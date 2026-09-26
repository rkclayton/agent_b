package events

import (
	"sync"
)

type Bus struct {
	mu          sync.Mutex
	publishMu   sync.Mutex
	seq         int64
	subscribers map[int]chan Event
	next        int
	sink        func(Event) error
	durableSink func(Event) (LogCursor, error)
	afterAppend func(Event, LogCursor)
	appendError func(Event, error)
	// Item 2ji (a): the run's time buckets have to be ON the run.stopped event,
	// because the wire is what the client, the journal and the telemetry receiver
	// all read. The enricher runs before the sink, so the fields are in the
	// journal too and not only in the live stream.
	enrich func(*Event)
}

func NewBus() *Bus                            { return &Bus{subscribers: map[int]chan Event{}} }
func (b *Bus) SetSink(sink func(Event) error) { b.mu.Lock(); b.sink = sink; b.mu.Unlock() }
func (b *Bus) SetDurableSink(sink func(Event) (LogCursor, error), appended func(Event, LogCursor), failed func(Event, error)) {
	b.mu.Lock()
	b.durableSink, b.afterAppend, b.appendError = sink, appended, failed
	b.mu.Unlock()
}

// SetEnricher installs a hook that may add fields to an event before it is
// sequenced, written or delivered. It sees every event; it must not block.
func (b *Bus) SetEnricher(enrich func(*Event)) {
	b.mu.Lock()
	b.enrich = enrich
	b.mu.Unlock()
}

func (b *Bus) Publish(event Event) Event {
	b.publishMu.Lock()
	published, dropped := b.publish(event, true)
	b.publishMu.Unlock()
	for _, id := range dropped {
		b.Publish(New(SubscriberDropped, "", "", map[string]any{"subscriber_id": id, "reason": "overflow", "action": "resubscribe"}))
	}
	return published
}
func (b *Bus) publish(event Event, writeSink bool) (Event, []int) {
	b.mu.Lock()
	enrich := b.enrich
	b.mu.Unlock()
	if enrich != nil {
		enrich(&event)
	}
	b.mu.Lock()
	b.seq++
	event.Seq = b.seq
	if event.TS == "" {
		event = New(event.Type, event.SessionID, event.RunID, event.Data)
		event.Seq = b.seq
	}
	type subscriber struct {
		id int
		ch chan Event
	}
	subscribers := make([]subscriber, 0, len(b.subscribers))
	for id, ch := range b.subscribers {
		subscribers = append(subscribers, subscriber{id, ch})
	}
	sink, durableSink, afterAppend, appendError := b.sink, b.durableSink, b.afterAppend, b.appendError
	b.mu.Unlock()
	var sinkErr error
	var cursor LogCursor
	if writeSink && sink != nil {
		sinkErr = sink(event)
	} else if writeSink && durableSink != nil {
		cursor, sinkErr = durableSink(event)
	}
	if sinkErr == nil && event.SessionID != "" && cursor.Offset > 0 && afterAppend != nil {
		afterAppend(event, cursor)
	} else if sinkErr != nil && appendError != nil {
		appendError(event, sinkErr)
	}
	dropped := make([]int, 0)
	for _, subscriber := range subscribers {
		b.mu.Lock()
		current, ok := b.subscribers[subscriber.id]
		if !ok || current != subscriber.ch {
			b.mu.Unlock()
			continue
		}
		select {
		case current <- event:
		default:
			delete(b.subscribers, subscriber.id)
			close(current)
			dropped = append(dropped, subscriber.id)
		}
		b.mu.Unlock()
	}
	if sinkErr != nil {
		message := sinkErr.Error()
		if event.Type == RunStopped {
			message = "outcome not saved: " + message
		}
		_, errorDrops := b.publish(New(Error, event.SessionID, event.RunID, map[string]any{"where": "event_log", "message": message, "lost_event_type": event.Type}), false)
		dropped = append(dropped, errorDrops...)
	}
	return event, dropped
}
func (b *Bus) Subscribe() (chan Event, func()) {
	b.mu.Lock()
	id := b.next
	b.next++
	ch := make(chan Event, 128)
	b.subscribers[id] = ch
	b.mu.Unlock()
	var once sync.Once
	return ch, func() { once.Do(func() { b.mu.Lock(); delete(b.subscribers, id); b.mu.Unlock() }) }
}

func (b *Bus) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}
