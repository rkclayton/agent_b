package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"harness/internal/agent"
	"harness/internal/chatstore"
	"harness/internal/config"
	"harness/internal/cron"
	"harness/internal/events"
	"harness/internal/session"
	webserver "harness/internal/web"
)

type scheduledRuns struct {
	profile   string
	cfg       func() config.Config
	registry  *session.Registry
	scheduler *agent.Scheduler
	bus       *events.Bus
	web       *webserver.Server
	mu        sync.Mutex
	sessions  map[string]string
}

func (r *scheduledRuns) setProfile(root string) { r.mu.Lock(); r.profile = root; r.mu.Unlock() }

func (r *scheduledRuns) run(ctx context.Context, job cron.Job) cron.Result {
	r.mu.Lock()
	profile := r.profile
	r.mu.Unlock()
	item, err := r.registry.Create(job.Name, r.cfg().DefaultAgentID(), "")
	if err != nil {
		return cron.Result{Failed: true, Failure: err.Error()}
	}
	r.mu.Lock()
	if r.sessions == nil {
		r.sessions = map[string]string{}
	}
	r.sessions[job.ID] = item.ID
	r.mu.Unlock()
	item.Origin = "scheduled"
	item.ScheduledFailure = job.LastFailure
	item.ToggleTool("cronjob", false)
	store := chatstore.New(filepath.Join(profile, "chats"))
	if err = os.MkdirAll(filepath.Join(profile, "chats", "Scheduled"), 0700); err != nil {
		return cron.Result{Failed: true, Failure: err.Error()}
	}
	if _, err = store.Move(item.ID, "Scheduled"); err != nil {
		return cron.Result{Failed: true, Failure: err.Error()}
	}
	if entries, scanErr := store.Scan(); scanErr == nil {
		r.registry.ReconcileChatHomes(entries)
	}
	prompt := strings.Builder{}
	for _, name := range job.Skills {
		if filepath.Base(name) != name || name == "." || name == "" {
			return cron.Result{Failed: true, Failure: fmt.Sprintf("skill %q is invalid", name)}
		}
		body, readErr := os.ReadFile(filepath.Join(profile, "skills", name, "SKILL.md"))
		if readErr != nil {
			return cron.Result{Failed: true, Failure: fmt.Sprintf("load skill %s: %v", name, readErr)}
		}
		fmt.Fprintf(&prompt, "## Scheduled skill: %s\n%s\n\n", name, body)
	}
	prompt.WriteString(job.Prompt)
	stream, unsubscribe := r.bus.Subscribe()
	defer unsubscribe()
	result, err := r.scheduler.Submit(ctx, item.ID, prompt.String())
	if err != nil {
		return cron.Result{Failed: true, Failure: err.Error()}
	}
	max := time.Duration(r.cfg().Cron.MaxRunMinutes) * time.Minute
	timer := time.NewTimer(max)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			r.scheduler.StopReason(item.ID, "cron_timeout")
			return cron.Result{Failed: true, Failure: ctx.Err().Error()}
		case <-timer.C:
			r.scheduler.StopReason(item.ID, "cron_timeout")
		case event, ok := <-stream:
			if !ok {
				return cron.Result{Failed: true, Failure: "event stream closed"}
			}
			if event.Type != events.RunStopped || event.SessionID != item.ID || (result.RunID != "" && event.RunID != result.RunID) {
				continue
			}
			data, _ := event.Data.(map[string]any)
			reason, _ := data["reason"].(string)
			answer := ""
			messages := item.Snapshot().Messages
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Role == "assistant" {
					answer = messages[i].Content
					break
				}
			}
			failed := reason != "done"
			failure := ""
			if failed {
				failure = reason
				if detail, ok := data["detail"].(string); ok && detail != "" {
					failure += " - " + detail
				}
			}
			return cron.Result{Answer: answer, Failed: failed, Failure: failure}
		}
	}
}

func (r *scheduledRuns) finish(job cron.Job, _ cron.Result, keep bool) {
	r.mu.Lock()
	id := r.sessions[job.ID]
	delete(r.sessions, job.ID)
	r.mu.Unlock()
	if !keep && id != "" {
		_ = r.web.DeleteChatHeadless(id)
	}
}
