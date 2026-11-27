package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/cron"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/skills"
	webserver "harness/internal/web"
)

type scheduledRuns struct {
	profile   string
	cfg       func() config.Config
	registry  *session.Registry
	scheduler *agent.Scheduler
	bus       *events.Bus
	web       *webserver.Server
	timer     func(time.Duration) scheduledAlarm
	mu        sync.Mutex
}

type scheduledAlarm struct {
	expired <-chan time.Time
	reset   func(time.Duration) bool
	stop    func() bool
}

func newScheduledAlarm(duration time.Duration) scheduledAlarm {
	timer := time.NewTimer(duration)
	return scheduledAlarm{expired: timer.C, reset: timer.Reset, stop: timer.Stop}
}

func (r *scheduledRuns) setProfile(root string) { r.mu.Lock(); r.profile = root; r.mu.Unlock() }

func (r *scheduledRuns) run(ctx context.Context, job cron.Job) cron.Result {
	r.mu.Lock()
	profile := r.profile
	r.mu.Unlock()
	item, err := r.registry.CreateScheduled(job.Name, r.cfg().DefaultAgentID(), job.Unattended)
	if err != nil {
		return cron.Result{Failed: true, Failure: err.Error()}
	}
	prompt := strings.Builder{}
	for _, name := range job.Skills {
		if filepath.Base(name) != name || name == "." || name == "" {
			return cron.Result{Failed: true, Failure: fmt.Sprintf("skill %q is invalid", name)}
		}
		body, skillPath, statePath, readErr := skills.Load(filepath.Join(profile, "skills"), filepath.Join(profile, "skill-state"), name)
		if readErr != nil {
			return cron.Result{Failed: true, Failure: fmt.Sprintf("load skill %s: %v", name, readErr)}
		}
		fmt.Fprintf(&prompt, "## Scheduled skill: %s\nSKILL.md: %s\n", name, filepath.ToSlash(skillPath))
		if statePath != "" {
			fmt.Fprintf(&prompt, "STATE FOLDER: %s\n", filepath.ToSlash(statePath))
		}
		fmt.Fprintf(&prompt, "%s\n\n", body)
	}
	prompt.WriteString(job.Prompt)
	stream := r.bus.Listen(ctx, "scheduled runs")
	result, err := r.scheduler.SubmitAttachmentsFrom(ctx, item.ID, prompt.String(), nil, "scheduled")
	if err != nil {
		return cron.Result{Failed: true, Failure: err.Error()}
	}
	max := time.Duration(r.cfg().Cron.MaxRunMinutes) * time.Minute
	const modelAvailabilityLimit = 30 * time.Minute
	makeAlarm := r.timer
	if makeAlarm == nil {
		makeAlarm = newScheduledAlarm
	}
	timer := makeAlarm(modelAvailabilityLimit)
	defer timer.stop()
	working := false
	shutdown := func() cron.Result {
		r.scheduler.StopReason(item.ID, "cron_shutdown")
		return cron.Result{Failed: true, Interrupted: true, Failure: "application closed", ChatID: item.ID}
	}
	for {
		select {
		case <-ctx.Done():
			return shutdown()
		case <-timer.expired:
			reason := "model_unreachable"
			if working {
				reason = "cron_timeout"
			}
			r.scheduler.StopReason(item.ID, reason)
		case event, ok := <-stream:
			if !ok {
				if ctx.Err() != nil {
					return shutdown()
				}
				return cron.Result{Failed: true, Failure: "event stream closed", ChatID: item.ID}
			}
			if event.SessionID != item.ID || (result.RunID != "" && event.RunID != result.RunID) {
				continue
			}
			if !working && (event.Type == events.ModelDelta || event.Type == events.ModelResponse) {
				working = true
				timer.reset(max)
			}
			if event.Type != events.RunStopped {
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
			return cron.Result{Answer: answer, Failed: failed, Failure: failure, Retryable: !working && reason == "model_unreachable", Interrupted: reason == "cron_shutdown", ChatID: item.ID}
		}
	}
}

func (r *scheduledRuns) finish(job cron.Job, result cron.Result, keep bool) {
	id := result.ChatID
	if result.Notify {
		reason := "done"
		if result.Failed {
			reason = "failed"
		}
		data := events.WithHuman(events.ScheduledFinished, map[string]any{"scheduled_job": job.Name, "reason": reason, "scheduled_answer": scheduledNoticeAnswer(result.Answer)})
		delete(data, "scheduled_answer")
		r.bus.PublishTransient(events.New(events.ScheduledFinished, id, "", data))
	}
	if !keep && id != "" {
		_ = r.web.DeleteChatHeadless(id)
	}
}

func scheduledNoticeAnswer(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) > 2 {
		lines = lines[:2]
	}
	answer := strings.TrimSpace(strings.Join(lines, "\n"))
	runes := []rune(answer)
	if len(runes) > 300 {
		answer = string(runes[:300])
	}
	return answer
}

// modelClassLocked answers local or remote for the session's connection. It is
// deliberately coarse: the contract forbids the endpoint, the model name
// and the connection id from leaving, and this is what is left that is useful.
