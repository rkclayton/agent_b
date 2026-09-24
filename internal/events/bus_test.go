package events

import (
	"fmt"
	"testing"
	"time"
)

func TestConcurrentPublishDoesNotSendOnOverflowClosedSubscriber(t *testing.T) {
	bus := NewBus()
	_, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	entered := make(chan struct{})
	release := make(chan struct{})
	bus.SetSink(func(event Event) error {
		if event.RunID == "blocked" {
			close(entered)
			<-release
		}
		return nil
	})
	panicValue := make(chan any, 1)
	go func() {
		defer func() { panicValue <- recover() }()
		bus.Publish(New(ModelDelta, "main", "blocked", map[string]any{"text": "blocked"}))
	}()
	<-entered
	filled := make(chan struct{})
	go func() {
		for index := 0; index < 129; index++ {
			bus.Publish(New(ModelDelta, "main", "filler", map[string]any{"text": "x"}))
		}
		close(filled)
	}()
	select {
	case <-filled:
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-filled
	if recovered := <-panicValue; recovered != nil {
		t.Fatalf("concurrent publisher panicked after subscriber overflow: %v", recovered)
	}
}

func TestRunStoppedWriteFailurePublishesOutcomeNotSaved(t *testing.T) {
	bus := NewBus()
	stream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	bus.SetSink(func(event Event) error {
		if event.Type == RunStopped {
			return fmt.Errorf("disk refused write")
		}
		return nil
	})
	bus.Publish(New(RunStopped, "s1", "r1", map[string]any{"reason": "done"}))
	<-stream
	failure := <-stream
	data, ok := failure.Data.(map[string]any)
	if !ok || failure.Type != Error || data["message"] != "outcome not saved: disk refused write" {
		t.Fatalf("failure event=%+v", failure)
	}
}

func TestConcurrentPublishAppendsInSequenceOrder(t *testing.T) {
	bus := NewBus()
	entered := make(chan struct{})
	release := make(chan struct{})
	appended := make(chan int64, 2)
	bus.SetSink(func(event Event) error {
		if event.RunID == "blocked" {
			close(entered)
			<-release
		}
		appended <- event.Seq
		return nil
	})
	done := make(chan struct{})
	go func() {
		bus.Publish(New(ModelDelta, "main", "blocked", map[string]any{"text": "first"}))
		close(done)
	}()
	<-entered
	secondDone := make(chan struct{})
	go func() {
		bus.Publish(New(ModelDelta, "main", "second", map[string]any{"text": "second"}))
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-secondDone
	first := <-appended
	second := <-appended
	<-done
	if first != 1 || second != 2 {
		t.Fatalf("sink append order = [%d %d], want [1 2]", first, second)
	}
}

func BenchmarkPublish(b *testing.B) {
	bus := NewBus()
	bus.SetSink(func(Event) error { return nil })
	event := New(ModelDelta, "main", "run", map[string]any{"text": "x"})
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		bus.Publish(event)
	}
}

func TestSinkReceivesDiagnosticPayloads(t *testing.T) {
	bus := NewBus()
	var logged Event
	bus.SetSink(func(event Event) error { logged = event; return nil })
	event := New(ModelResponse, "main", "r1", map[string]any{"content": "done"})
	event.Body, event.Raw = map[string]any{"messages": []string{"request"}}, "raw model stream"
	published := bus.Publish(event)
	if published.Body == nil || published.Raw == nil || logged.Body == nil || logged.Raw == nil {
		t.Fatal("diagnostic payload was removed before persistence")
	}
}

func TestSinkFailureBecomesOperationalErrorAndMarksProjectionStale(t *testing.T) {
	bus := NewBus()
	seen := make(chan Event, 4)
	stream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	bus.SetDurableSink(func(Event) (LogCursor, error) { return LogCursor{}, fmt.Errorf("disk full") }, nil, func(event Event, err error) { seen <- event })
	bus.Publish(New(ToolResult, "main", "run", map[string]any{"name": "shell"}))
	if stale := <-seen; stale.Type != ToolResult {
		t.Fatalf("stale event=%s", stale.Type)
	}
	first, second := <-stream, <-stream
	if first.Type != ToolResult || second.Type != Error {
		t.Fatalf("stream=%s,%s", first.Type, second.Type)
	}
	data := second.Data.(map[string]any)
	if data["where"] != "event_log" || data["lost_event_type"] != ToolResult {
		t.Fatalf("error data=%#v", data)
	}
}

func TestSubscriberOverflowClosesForResync(t *testing.T) {
	bus := NewBus()
	var logged []Event
	bus.SetSink(func(event Event) error { logged = append(logged, event); return nil })
	stream, _ := bus.Subscribe()
	for index := 0; index < 130; index++ {
		bus.Publish(New(ModelDelta, "main", "r1", map[string]any{"turn": 1, "text": "x"}))
	}
	for range 128 {
		<-stream
	}
	if _, ok := <-stream; ok {
		t.Fatal("overflowed subscriber remained open")
	}
	var drop Event
	for _, event := range logged {
		if event.Type == SubscriberDropped {
			drop = event
		}
	}
	if drop.Type == "" {
		t.Fatal("subscriber drop was not journaled")
	}
	data, _ := drop.Data.(map[string]any)
	if data["reason"] != "overflow" || data["action"] != "resubscribe" {
		t.Fatalf("drop event data = %#v", data)
	}
}

func TestSubscriberCountTracksSubscribeAndUnsubscribe(t *testing.T) {
	bus := NewBus()
	if got := bus.SubscriberCount(); got != 0 {
		t.Fatalf("initial subscriber count = %d", got)
	}
	_, unsubscribeFirst := bus.Subscribe()
	_, unsubscribeSecond := bus.Subscribe()
	if got := bus.SubscriberCount(); got != 2 {
		t.Fatalf("subscribed count = %d", got)
	}
	unsubscribeFirst()
	if got := bus.SubscriberCount(); got != 1 {
		t.Fatalf("after first unsubscribe = %d", got)
	}
	unsubscribeFirst()
	unsubscribeSecond()
	if got := bus.SubscriberCount(); got != 0 {
		t.Fatalf("final subscriber count = %d", got)
	}
}
