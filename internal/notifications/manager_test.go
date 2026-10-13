package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/events"
)

func TestTwoEventsProduceTwoHumanPostsWithoutDuplicates(t *testing.T) {
	var mu sync.Mutex
	var messages []string
	mentionsDisabled := true
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Content         string `json:"content"`
			AllowedMentions struct {
				Parse []string `json:"parse"`
			} `json:"allowed_mentions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		messages = append(messages, body.Content)
		mentionsDisabled = mentionsDisabled && body.AllowedMentions.Parse != nil && len(body.AllowedMentions.Parse) == 0
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	bus := events.NewBus()
	manager := New(bus, func(string) string { return "Build chat" }, "http://127.0.0.1:8790")
	manager.allowLocal = true
	if err := manager.Configure(receiver.URL); err != nil {
		t.Fatal(err)
	}
	manager.Start(context.Background())
	defer manager.Close()
	bus.Publish(events.New(events.ApprovalRequired, "s one", "r1", events.WithHuman(events.ApprovalRequired, map[string]any{"name": "shell"})))
	bus.Publish(events.New(events.RunStopped, "s one", "r1", events.WithHuman(events.RunStopped, map[string]any{"reason": "done"})))
	bus.Publish(events.New(events.RunStopped, "watch", "r2", events.WithHuman(events.RunStopped, map[string]any{"reason": "done", "scheduled_job": "price-watch desk", "scheduled_answer": "899 USD\nhttps://shop.invalid/item"})))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(messages)
		mu.Unlock()
		if count == 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(messages) != 3 {
		t.Fatalf("posts=%d messages=%q", len(messages), messages)
	}
	if !mentionsDisabled {
		t.Fatal("Discord allowed_mentions was not explicitly disabled")
	}
	if !strings.Contains(messages[0], "shell needs your approval") || !strings.Contains(messages[0], "http://127.0.0.1:8790/chat?session=s+one") {
		t.Fatalf("approval message=%q", messages[0])
	}
	if !strings.Contains(messages[1], "The run finished.") {
		t.Fatalf("stopped message=%q", messages[1])
	}
	if !strings.Contains(messages[2], "price-watch desk") || !strings.Contains(messages[2], "899 USD\nhttps://shop.invalid/item") || strings.Contains(messages[2], "Scheduled chat folder") {
		t.Fatalf("scheduled message=%q", messages[2])
	}
}

func TestReleaseGateDeliversRunItemPlanWorkerAndSettingsTestOnceEach(t *testing.T) {
	var mu sync.Mutex
	var messages []string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		messages = append(messages, body.Content)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	bus := events.NewBus()
	manager := New(bus, func(id string) string { return id }, "http://127.0.0.1:8790")
	manager.allowLocal = true
	if err := manager.Configure(receiver.URL); err != nil {
		t.Fatal(err)
	}
	manager.Start(context.Background())
	defer manager.Close()

	for _, event := range []events.Event{
		events.New(events.RunStopped, "run-chat", "r1", events.WithHuman(events.RunStopped, map[string]any{"reason": "done"})),
		events.New(events.ItemDone, "", "", events.WithHuman(events.ItemDone, map[string]any{"item_id": "2hh"})),
		events.New(events.PlanDone, "", "", events.WithHuman(events.PlanDone, map[string]any{"plan_id": "release"})),
		events.New(events.WorkerJob, "worker", "", events.WithHuman(events.WorkerJob, map[string]any{"role": "c", "plan_id": "release"})),
	} {
		bus.Publish(event)
	}
	if _, err := manager.SendTest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(messages)
		mu.Unlock()
		if count == 5 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(messages) != 5 {
		t.Fatalf("posts=%d messages=%q", len(messages), messages)
	}
	for _, fragment := range []string{"The run finished.", "item", "plan", "/plan", "Agent_b test notification."} {
		count := 0
		for _, message := range messages {
			if strings.Contains(strings.ToLower(message), strings.ToLower(fragment)) {
				count++
			}
		}
		if count == 0 {
			t.Errorf("no delivery contains %q: %q", fragment, messages)
		}
	}
}

func TestChangingConfigurationCancelsWaitingRetry(t *testing.T) {
	bus := events.NewBus()
	manager := New(bus, nil, "http://127.0.0.1:8790")
	manager.allowLocal = true
	if err := manager.Configure("http://127.0.0.1:1/first"); err != nil {
		t.Fatal(err)
	}
	attempted := make(chan struct{}, 1)
	manager.client = roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempted <- struct{}{}
		return nil, context.DeadlineExceeded
	})
	manager.wait = func(ctx context.Context, _ time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- manager.deliver(context.Background(), events.RunStopped, "old event") }()
	<-attempted
	if err := manager.Configure("http://127.0.0.1:2/second"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("delivery error=%v", err)
	}
	select {
	case <-attempted:
		t.Fatal("old event retried after its configured destination changed")
	default:
	}
}

func TestNoURLProducesNoPostsAndNoFailureEvent(t *testing.T) {
	bus := events.NewBus()
	stream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	manager := New(bus, nil, "http://127.0.0.1:8790")
	manager.Start(context.Background())
	defer manager.Close()
	bus.Publish(events.New(events.RunStopped, "s1", "r1", map[string]any{"reason": "done"}))
	time.Sleep(20 * time.Millisecond)
	for {
		select {
		case event := <-stream:
			if event.Type == events.NotificationFailed {
				t.Fatalf("unexpected failure event: %+v", event)
			}
		default:
			return
		}
	}
}

func TestFailureRetriesOnceAfterThirtySecondsAndLogsSafeReason(t *testing.T) {
	bus := events.NewBus()
	stream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	manager := New(bus, nil, "http://127.0.0.1:8790")
	manager.allowLocal = true
	if err := manager.Configure("http://127.0.0.1:1/hook-secret"); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	manager.client = roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, context.DeadlineExceeded
	})
	var waited time.Duration
	manager.wait = func(_ context.Context, delay time.Duration) error { waited = delay; return nil }
	err := manager.deliver(context.Background(), events.RunStopped, "message")
	if err == nil || attempts != 2 || waited != 30*time.Second {
		t.Fatalf("err=%v attempts=%d waited=%s", err, attempts, waited)
	}
	for {
		event := <-stream
		if event.Type != events.NotificationFailed {
			continue
		}
		encoded, _ := json.Marshal(event.Data)
		data := event.Data.(map[string]any)
		if strings.Contains(string(encoded), "hook-secret") || !strings.Contains(data["message"].(string), "Settings > Notifications") {
			t.Fatalf("failure data=%s", encoded)
		}
		break
	}
}

func TestWebhookGuardRejectsRedirectableOrLookalikeTargets(t *testing.T) {
	for _, raw := range []string{
		"http://discord.com/api/webhooks/1/token",
		"https://discord.com.evil.example/api/webhooks/1/token",
		"https://discord.com/login",
		"https://user:someone@example.org/api/webhooks/1/token",
		"https://discord.com/api/webhooks/1/token?wait=true",
	} {
		if _, err := parseWebhook(raw, false); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if _, err := parseWebhook("https://discord.com/api/webhooks/1/token", false); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) Do(request *http.Request) (*http.Response, error) { return fn(request) }

// A worker has no chat, so the notification for its approval points at the
// Plan page, where its card is drawn, and still carries the human sentence.
func TestWorkerApprovalNotificationPointsAtThePlan(t *testing.T) {
	manager := New(events.NewBus(), func(string) string { return "worker" }, "http://127.0.0.1:8790")
	message := manager.message(events.New(events.ApprovalRequired, "c1", "r1", events.WithHuman(events.ApprovalRequired, map[string]any{"name": "read_file.operator_override", "boundary_escape": true, "role": "c", "plan_id": "p1"})))
	if !strings.Contains(message, "http://127.0.0.1:8790/plan") || strings.Contains(message, "/chat?session=c1") {
		t.Fatalf("worker approval link = %q", message)
	}
	if !strings.Contains(message, "needs your") {
		t.Fatalf("worker approval lost its human sentence: %q", message)
	}
}

type desktopStandIn struct {
	notices []DesktopNotice
}

func (d *desktopStandIn) Viewing(session string) bool { return session == "open" }
func (d *desktopStandIn) Select(string)               {}
func (d *desktopStandIn) Show(notice DesktopNotice) error {
	d.notices = append(d.notices, notice)
	return nil
}

type phoneStandIn struct {
	sent int
}

func (p *phoneStandIn) NotificationState() string { return "not paired" }
func (p *phoneStandIn) Notify(context.Context, string, string, string) (string, error) {
	p.sent++
	return "not paired", nil
}

func TestEveryNoticeFansOutOnceAndDesktopHonorsViewingAndSwitch2rz(t *testing.T) {
	bus := events.NewBus()
	desktop := &desktopStandIn{}
	phone := &phoneStandIn{}
	manager := New(bus, func(id string) string { return "title " + id }, "http://127.0.0.1:8790")
	desktopOn := true
	manager.SetChannels(desktop, phone, func(string) bool { return desktopOn })
	for _, kind := range []string{events.ApprovalRequired, events.RunStopped, events.ItemDone, events.ItemStuck, events.PlanDone, events.WorkerJob} {
		manager.deliverEvent(context.Background(), events.New(kind, "other", "r1", events.WithHuman(kind, map[string]any{"reason": "done", "scheduled_job": "job", "scheduled_answer": "one\ntwo\nthree"})))
	}
	manager.deliverEvent(context.Background(), events.New(events.RunStopped, "open", "r2", events.WithHuman(events.RunStopped, map[string]any{"reason": "done"})))
	if len(desktop.notices) != 6 || phone.sent != 7 {
		t.Fatalf("desktop=%d phone=%d", len(desktop.notices), phone.sent)
	}
	if notice := desktop.notices[1]; notice.Title != "job" || notice.Body == "" || strings.Count(notice.Body, "\n") > 1 || notice.ChatID != "other" {
		t.Fatalf("notice=%+v", notice)
	}
	answers, err := manager.SendTest(context.Background(), "")
	if err != nil || answers["desktop"] != "shown" || answers["phone"] != "not paired" || len(answers) != 2 {
		t.Fatalf("answers=%v err=%v", answers, err)
	}
	desktopOn = false
	manager.deliverEvent(context.Background(), events.New(events.RunStopped, "off", "r3", events.WithHuman(events.RunStopped, map[string]any{"reason": "done"})))
	if len(desktop.notices) != 7 || phone.sent != 9 {
		t.Fatalf("desktop=%d phone=%d", len(desktop.notices), phone.sent)
	}
}

func TestDesktopEventGroupsCanBeSwitchedIndependently2tx(t *testing.T) {
	desktop := &desktopStandIn{}
	manager := New(events.NewBus(), nil, "http://127.0.0.1:8790")
	enabled := map[string]bool{"needs_you": true, "finished": false, "scheduled": true}
	manager.SetChannels(desktop, nil, func(group string) bool { return group == "" || enabled[group] })
	for _, event := range []events.Event{
		events.New(events.ApprovalRequired, "a", "r", events.WithHuman(events.ApprovalRequired, map[string]any{})),
		events.New(events.RunStopped, "b", "r", events.WithHuman(events.RunStopped, map[string]any{"reason": "done"})),
		events.New(events.RunStopped, "c", "r", events.WithHuman(events.RunStopped, map[string]any{"reason": "done", "scheduled_job": "watch"})),
	} {
		manager.deliverEvent(context.Background(), event)
	}
	if len(desktop.notices) != 2 || desktop.notices[0].ChatID != "a" || desktop.notices[1].ChatID != "c" {
		t.Fatalf("notices=%+v", desktop.notices)
	}
}

func TestOnlyLatestChatNotifiesAndModesOverrideIt2re(t *testing.T) {
	desktop := &desktopStandIn{}
	phone := &phoneStandIn{}
	posts := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts++ }))
	defer receiver.Close()
	manager := New(events.NewBus(), nil, "http://127.0.0.1:8790")
	manager.allowLocal = true
	if err := manager.Configure(receiver.URL); err != nil {
		t.Fatal(err)
	}
	manager.SetChannels(desktop, phone, func(string) bool { return true })
	persisted := 0
	manager.SetChatPolicy(map[string]ChatNoticeState{
		"first": {Notify: "auto"}, "second": {Notify: "auto"}, "third": {Notify: "auto"},
	}, func(string, string, time.Time) error { persisted++; return nil })
	for index, id := range []string{"first", "third", "second"} {
		manager.observe(events.Event{TS: time.Date(2026, 10, 13, 12, index, 0, 0, time.UTC).Format(time.RFC3339Nano), SessionID: id, Type: events.MessageAppended, Data: map[string]any{"message": events.Message{Role: "user"}}})
	}
	manager.observe(events.Event{TS: time.Date(2026, 10, 13, 11, 0, 0, 0, time.UTC).Format(time.RFC3339Nano), SessionID: "first", Type: events.MessageAppended, Data: map[string]any{"message": events.Message{Role: "user"}}})
	if persisted != 3 {
		t.Fatalf("persisted=%d", persisted)
	}
	for _, id := range []string{"first", "second", "third"} {
		manager.deliverEvent(context.Background(), events.New(events.RunStopped, id, "run", map[string]any{"reason": "done"}))
	}
	if len(desktop.notices) != 1 || desktop.notices[0].ChatID != "second" || phone.sent != 1 || posts != 1 {
		t.Fatalf("desktop=%+v phone=%d posts=%d", desktop.notices, phone.sent, posts)
	}
	if err := manager.SetChatMode("second", "off"); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetChatMode("third", "on"); err != nil {
		t.Fatal(err)
	}
	manager.deliverEvent(context.Background(), events.New(events.RunStopped, "second", "run", map[string]any{"reason": "done"}))
	manager.deliverEvent(context.Background(), events.New(events.RunStopped, "third", "run", map[string]any{"reason": "done"}))
	manager.deliverEvent(context.Background(), events.New(events.RunStopped, "second", "run", map[string]any{"reason": "done", "scheduled_job": "watch"}))
	if len(desktop.notices) != 3 || phone.sent != 3 || posts != 3 {
		t.Fatalf("overrides desktop=%+v phone=%d posts=%d", desktop.notices, phone.sent, posts)
	}
}
