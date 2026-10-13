package events

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
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

func TestTransientEventReachesLiveSinkWithoutDurableWrite2qf(t *testing.T) {
	bus := NewBus()
	durable := 0
	bus.SetDurableSink(func(Event) (LogCursor, error) { durable++; return LogCursor{}, nil }, nil, nil)
	seen := ""
	bus.SetTransientSink(func(event Event) { seen = event.Type })
	bus.PublishTransient(New(ToolProgress, "s1", "r1", map[string]any{"line": "latest"}))
	if durable != 0 || seen != ToolProgress {
		t.Fatalf("durable=%d seen=%q", durable, seen)
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

func TestNamedListenersRecoverAfterOverflow2um(t *testing.T) {
	bus := NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	streams := map[string]<-chan Event{}
	for _, name := range []string{"terminal", "scheduled runs", "file retention", "worker", "notifications", "activity totals", "connection health", "reflection", "telemetry"} {
		streams[name] = bus.Listen(ctx, name)
	}
	for index := 0; index < 1000; index++ {
		bus.Publish(New(ModelDelta, "main", "flood", map[string]any{"text": "x"}))
	}
	time.Sleep(5 * time.Second) // every consumer remains held while its bus queue overflows
	reached := make(chan string, len(streams))
	for name, stream := range streams {
		go func() {
			for got := range stream {
				if got.RunID == "after" {
					reached <- name
					return
				}
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	post := bus.Publish(New(RunStopped, "main", "after", map[string]any{"reason": "done"}))
	_ = post
	for range streams {
		select {
		case <-reached:
		case <-time.After(time.Second):
			t.Fatal("a recovered listener did not receive the post-flood event")
		}
	}
}

func TestNamedListenerDropLogsItsNameOnce2um(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	bus := NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = bus.Listen(ctx, "notifications")
	deadline := time.Now().Add(time.Second)
	for bus.SubscriberCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	for index := 0; index < 1000 && !strings.Contains(output.String(), "notifications"); index++ {
		bus.Publish(New(ModelDelta, "main", "flood", map[string]any{"text": "x"}))
	}
	if got := strings.Count(output.String(), `event listener "notifications" dropped`); got != 1 {
		t.Fatalf("named drop log count=%d log=%q", got, output.String())
	}
}

func TestConnectionListenerSignalsOverflow2um(t *testing.T) {
	bus := NewBus()
	stream, dropped, unsubscribe := bus.ListenUntilDrop("phone")
	defer unsubscribe()
	for index := 0; index < 1000; index++ {
		bus.Publish(New(ModelDelta, "main", "flood", map[string]any{"text": "x"}))
	}
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("phone listener did not signal its dropped connection")
	}
	for range 128 {
		<-stream
	}
	if _, ok := <-stream; ok {
		t.Fatal("phone stream remained connected after overflow")
	}
}
