package events

import (
	"context"
	"log"
	"sync"
)

type Bus struct {
	mu            sync.Mutex
	publishMu     sync.Mutex
	seq           int64
	subscribers   map[int]chan Event
	dropped       map[int]chan struct{}
	listenerNames map[int]string
	next          int
	sink          func(Event) error
	durableSink   func(Event) (LogCursor, error)
	afterAppend   func(Event, LogCursor)
	appendError   func(Event, error)
	transientSink func(Event)
	// Item 2ji (a): the run's time buckets have to be ON the run.stopped event,
	// because the wire is what the client, the journal and the telemetry receiver
	// all read. The enricher runs before the sink, so the fields are in the
	// journal too and not only in the live stream.
	enrich func(*Event)
	// Item 2pw: the flight recorder sees every event in order on the publisher's
	// goroutine, so it cannot be dropped the way a slow channel subscriber is. It
	// must not publish and must cost the same however much it has stored.
	observe func(Event)
}

// SetObserver installs the flight recorder's hook (item 2pw).
func (b *Bus) SetObserver(observe func(Event)) { b.mu.Lock(); b.observe = observe; b.mu.Unlock() }

func NewBus() *Bus {
	return &Bus{subscribers: map[int]chan Event{}, dropped: map[int]chan struct{}{}, listenerNames: map[int]string{}}
}
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
	for _, subscriber := range dropped {
		if subscriber.name != "" {
			log.Printf("event listener %q dropped: queue overflow; resubscribing", subscriber.name)
		}
		b.Publish(New(SubscriberDropped, "", "", map[string]any{"subscriber_id": subscriber.id, "reason": "overflow", "action": "resubscribe"}))
	}
	if published.Type == RunStarted || published.Type == RunQueued {
		clock := New(ToolProgress, published.SessionID, published.RunID, map[string]any{"run_started": true})
		clock.TS = published.TS
		b.PublishTransient(clock)
	} else if published.Type == ToolCallEvent {
		data, _ := published.Data.(map[string]any)
		progress := New(ToolProgress, published.SessionID, published.RunID, map[string]any{"tool_started": true, "args": data["args"]})
		progress.TS = published.TS
		b.PublishTransient(progress)
	}
	return published
}
func (b *Bus) SetTransientSink(sink func(Event)) { b.mu.Lock(); b.transientSink = sink; b.mu.Unlock() }

// PublishTransient delivers replace-in-place progress without journaling it.
func (b *Bus) PublishTransient(event Event) Event {
	b.publishMu.Lock()
	published, dropped := b.publish(event, false)
	b.publishMu.Unlock()
	b.mu.Lock()
	sink := b.transientSink
	b.mu.Unlock()
	if sink != nil {
		sink(published)
	}
	for _, subscriber := range dropped {
		if subscriber.name != "" {
			log.Printf("event listener %q dropped: queue overflow; resubscribing", subscriber.name)
		}
		b.Publish(New(SubscriberDropped, "", "", map[string]any{"subscriber_id": subscriber.id, "reason": "overflow", "action": "resubscribe"}))
	}
	return published
}

type droppedSubscriber struct {
	id   int
	name string
}

func (b *Bus) publish(event Event, writeSink bool) (Event, []droppedSubscriber) {
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
	sink, durableSink, afterAppend, appendError, observe := b.sink, b.durableSink, b.afterAppend, b.appendError, b.observe
	b.mu.Unlock()
	if observe != nil {
		observe(event)
	}
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
	dropped := make([]droppedSubscriber, 0)
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
			drop := b.dropped[subscriber.id]
			delete(b.dropped, subscriber.id)
			close(current)
			if drop != nil {
				close(drop)
			}
			dropped = append(dropped, droppedSubscriber{id: subscriber.id, name: b.listenerNames[subscriber.id]})
			delete(b.listenerNames, subscriber.id)
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
	ch, _, unsubscribe := b.subscribe("")
	return ch, unsubscribe
}

func (b *Bus) subscribe(name string) (chan Event, <-chan struct{}, func()) {
	b.mu.Lock()
	id := b.next
	b.next++
	ch := make(chan Event, 128)
	dropped := make(chan struct{})
	b.subscribers[id] = ch
	b.dropped[id] = dropped
	b.listenerNames[id] = name
	b.mu.Unlock()
	var once sync.Once
	return ch, dropped, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, id)
			delete(b.dropped, id)
			delete(b.listenerNames, id)
			b.mu.Unlock()
		})
	}
}

// Listen keeps a named process listener subscribed. If its fixed 128-event bus
// queue overflows, queued events are abandoned and a fresh subscription is made;
// events are never replayed or reordered.
func (b *Bus) Listen(ctx context.Context, name string) <-chan Event {
	out := make(chan Event, 128)
	in, dropped, unsubscribe := b.subscribe(name)
	go func() {
		defer close(out)
		for ctx.Err() == nil {
			for {
				select {
				case <-ctx.Done():
					unsubscribe()
					return
				case <-dropped:
					unsubscribe()
					goto resubscribe
				case event, ok := <-in:
					if !ok {
						unsubscribe()
						goto resubscribe
					}
					select {
					case out <- event:
					case <-dropped:
						unsubscribe()
						goto resubscribe
					case <-ctx.Done():
						unsubscribe()
						return
					}
				}
			}
		resubscribe:
			if ctx.Err() == nil {
				in, dropped, unsubscribe = b.subscribe(name)
			}
		}
	}()
	return out
}

// ListenUntilDrop is for a listener whose owning connection must be ended on
// overflow. The caller resubscribes by establishing that connection again.
func (b *Bus) ListenUntilDrop(name string) (chan Event, <-chan struct{}, func()) {
	return b.subscribe(name)
}

func (b *Bus) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}
