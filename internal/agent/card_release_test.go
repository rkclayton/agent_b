package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

// cardRig is two chats on one model profile (limit one) whose every run asks
// for list_dir under approval mode "all", so each run pauses on a card.
type cardRig struct {
	t         *testing.T
	scheduler *Scheduler
	runner    *Runner
	a, b      *session.Session
	mu        sync.Mutex
	cards     map[string]string // session -> call id of its live card
	stopped   map[string]int
	queuedFor map[string]map[string]any
	changed   chan struct{}
}

func newCardRig(t *testing.T) *cardRig {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		var delta map[string]any
		finish := "stop"
		if n := len(body.Messages); n > 0 && body.Messages[n-1].Role == "tool" {
			delta = map[string]any{"content": "done"}
		} else {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call-%d", time.Now().UnixNano()), "type": "function", "function": map[string]any{"name": "list_dir", "arguments": `{"path":"."}`}}}}
			finish = "tool_calls"
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	t.Cleanup(model.Close)
	workspace := t.TempDir()
	cfg := config.Defaults(workspace)
	cfg.Context.Accounting = "estimated"
	cfg.Approval.Mode = config.ApprovalModeAll
	cfg.Run.MaxConcurrent = 2
	profile := cfg.Servers[0]
	profile.ID, profile.BaseURL, profile.Model = "main", model.URL, "fake"
	profile.Context.NCtx, profile.Context.ReserveOutput = 32768, 8192
	profile.Capabilities.Streaming, profile.Capabilities.ToolCalls = true, true
	profile.Capabilities.OverflowBehavior = "error"
	cfg.Servers = []config.Profile{profile}
	rig := &cardRig{t: t, cards: map[string]string{}, stopped: map[string]int{}, queuedFor: map[string]map[string]any{}, changed: make(chan struct{}, 64)}
	bus := events.NewBus()
	bus.SetSink(func(event events.Event) error {
		rig.mu.Lock()
		switch event.Type {
		case events.ApprovalRequired:
			rig.cards[event.SessionID] = event.Data.(map[string]any)["call_id"].(string)
		case events.ApprovalDecided:
			delete(rig.cards, event.SessionID)
		case events.RunStopped:
			rig.stopped[event.SessionID]++
			t.Logf("run.stopped %s %v", event.SessionID, event.Data)
		case events.RunQueued:
			rig.queuedFor[event.SessionID] = event.Data.(map[string]any)
		}
		rig.mu.Unlock()
		select {
		case rig.changed <- struct{}{}:
		default:
		}
		return nil
	})
	writers, err := events.NewWriters(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writers.Close() })
	lookup := func(id string) (*config.Profile, bool) { return &profile, id == profile.ID }
	current := func() config.Config { return cfg }
	registry := session.NewRegistry(bus, writers, lookup, cfg.Run.MaxTurns, current)
	rig.a, _ = registry.Create("a", profile.ID, workspace)
	rig.b, _ = registry.Create("b", profile.ID, workspace)
	if rig.a == nil || rig.b == nil {
		t.Fatal("sessions were not created")
	}
	rig.runner = NewRunner(bus, tools.New(), &PromptRenderer{text: "system"}, lookup, current)
	rig.scheduler = NewScheduler(rig.runner, registry, bus, current)
	return rig
}

// until waits for a condition on the rig's recorded events.
func (rig *cardRig) until(what string, ok func() bool) {
	rig.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		rig.mu.Lock()
		done := ok()
		rig.mu.Unlock()
		if done {
			return
		}
		select {
		case <-rig.changed:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			rig.t.Fatalf("timed out waiting for %s (cards %v, stopped %v)", what, rig.cards, rig.stopped)
		}
	}
}

func (rig *cardRig) answer(item *session.Session) {
	rig.t.Helper()
	rig.mu.Lock()
	callID := rig.cards[item.ID]
	rig.mu.Unlock()
	if err := rig.runner.Gate().Decide(item.ID, callID, "once"); err != nil {
		rig.t.Fatalf("answer %s: %v", item.ID, err)
	}
}

// Item 2fs: a run paused on an unanswered card holds no model slot, so another
// chat on the same profile runs; answering re-acquires the slot. The v0.70.2/W1
// question — can re-acquiring deadlock with a queued run that raises its own
// card? — is answered by driving exactly that: B is admitted while A waits,
// B pauses on its own card, and the two are answered in both orders.
func TestACardNobodyAnswersDoesNotHoldTheModel(t *testing.T) {
	for _, order := range []string{"a-then-b", "b-then-a"} {
		t.Run(order, func(t *testing.T) {
			rig := newCardRig(t)
			if _, err := rig.scheduler.Submit(context.Background(), rig.a.ID, "list the folder"); err != nil {
				t.Fatal(err)
			}
			rig.until("A's card", func() bool { return rig.cards[rig.a.ID] != "" })
			if _, err := rig.scheduler.Submit(context.Background(), rig.b.ID, "list the folder"); err != nil {
				t.Fatal(err)
			}
			rig.until("B running to its own card while A's card waits", func() bool { return rig.cards[rig.b.ID] != "" })
			first, second := rig.a, rig.b
			if order == "b-then-a" {
				first, second = rig.b, rig.a
			}
			rig.answer(first)
			rig.until("the first answered run to finish", func() bool { return rig.stopped[first.ID] == 1 })
			if second.Snapshot().Run.Status != "paused" {
				t.Fatalf("the unanswered run is %q, want paused", second.Snapshot().Run.Status)
			}
			rig.answer(second)
			rig.until("the second answered run to finish", func() bool { return rig.stopped[second.ID] == 1 })
			for _, item := range []*session.Session{rig.a, rig.b} {
				if run := item.Snapshot().Run; run.Status != "idle" || run.LastStopReason != "done" {
					t.Fatalf("%s ended %s/%s", item.ID, run.Status, run.LastStopReason)
				}
			}
		})
	}
}

// Answered while another run holds the model, a paused run waits for the slot
// rather than calling the model alongside it, then finishes.
func TestAnAnsweredRunWaitsForTheModelItReleased(t *testing.T) {
	rig := newCardRig(t)
	if _, err := rig.scheduler.Submit(context.Background(), rig.a.ID, "list the folder"); err != nil {
		t.Fatal(err)
	}
	rig.until("A's card", func() bool { return rig.cards[rig.a.ID] != "" })
	// B holds the model: an active run that is not paused.
	rig.scheduler.mu.Lock()
	holder := &activeRun{runID: "held", done: make(chan struct{})}
	rig.scheduler.active[rig.b.ID] = holder
	rig.scheduler.mu.Unlock()
	rig.answer(rig.a)
	time.Sleep(300 * time.Millisecond)
	rig.mu.Lock()
	finishedEarly := rig.stopped[rig.a.ID]
	rig.mu.Unlock()
	if finishedEarly != 0 {
		t.Fatal("the answered run called the model while another run held its only slot")
	}
	if data := rig.queuedFor[rig.a.ID]; data == nil || data["behind"] != "agent_b" {
		t.Fatalf("the waiting run's run.queued = %v, want behind agent_b", data)
	}
	rig.scheduler.mu.Lock()
	delete(rig.scheduler.active, rig.b.ID)
	rig.scheduler.drainLocked()
	rig.scheduler.mu.Unlock()
	rig.until("A to finish once the slot is free", func() bool { return rig.stopped[rig.a.ID] == 1 })
}
