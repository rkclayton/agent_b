package web

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
)

// Item 2jg (d): off means off, asserted at the level that matters — the BUS.
//
// internal/telemetry proves the sender does nothing when it does not exist. This
// proves it does not exist: with the switch off, the bus's subscriber count is
// unchanged, so there is no path from an event to a batch at all. That is the
// difference between "sends less" and "off", and it is the reason the assertion
// is about the bus rather than about a flag.
func TestOffLeavesNoSubscriberOnTheBus2jg(t *testing.T) {
	bus := events.NewBus()
	before := bus.SubscriberCount()

	server := &Server{bus: bus, cfg: &config.Config{}}
	server.roots.Profile = t.TempDir()

	off := config.Config{Telemetry: config.Telemetry{Enabled: false, Endpoint: "https://receiver.invalid/ingest"}}
	server.applyTelemetry(off)
	if got := bus.SubscriberCount(); got != before {
		t.Fatalf("telemetry off still subscribed: %d subscribers, was %d", got, before)
	}
	if server.TelemetryRunning() {
		t.Fatal("telemetry off reports itself running")
	}

	// And on with no endpoint is equally off, because there is nowhere to send:
	// collecting into a queue nobody drains is not a service to anyone.
	noEndpoint := config.Config{Telemetry: config.Telemetry{Enabled: true}}
	server.applyTelemetry(noEndpoint)
	if got := bus.SubscriberCount(); got != before {
		t.Fatalf("telemetry with no endpoint subscribed: %d subscribers, was %d", got, before)
	}
	if server.TelemetryRunning() {
		t.Fatal("telemetry with no endpoint reports itself running")
	}
}

// The positive control: with the switch on and an endpoint set, there IS a
// subscriber, an event reaches the transport, and turning it off again removes
// both the subscriber and the queue. Without this, the test above would pass on
// a build where telemetry never works at all.
func TestOnCollectsAndOffRemovesItAgain2jg(t *testing.T) {
	bus := events.NewBus()
	before := bus.SubscriberCount()

	var mu sync.Mutex
	sent := 0
	profile := t.TempDir()
	server := &Server{bus: bus, cfg: &config.Config{}}
	server.roots.Profile = profile
	server.telemetry.transport = func([]byte) error {
		mu.Lock()
		sent++
		mu.Unlock()
		return nil
	}

	on := config.Config{Telemetry: config.Telemetry{Enabled: true, Endpoint: "https://receiver.invalid/ingest", InstallID: "id"}}
	server.applyTelemetry(on)
	if got := bus.SubscriberCount(); got != before+1 {
		t.Fatalf("telemetry on did not subscribe: %d subscribers, was %d", got, before)
	}
	if !server.TelemetryRunning() {
		t.Fatal("telemetry on does not report itself running")
	}

	bus.Publish(events.New(events.RunStopped, "s1", "r1", map[string]any{"reason": "done", "turns": 2}))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.telemetry.mu.Lock()
		state := server.telemetry.state
		server.telemetry.mu.Unlock()
		if state != nil {
			state.sender.Flush()
		}
		mu.Lock()
		got := sent
		mu.Unlock()
		if got > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	got := sent
	mu.Unlock()
	if got == 0 {
		t.Fatal("telemetry on collected nothing, so the off assertion proves nothing")
	}

	server.applyTelemetry(config.Config{Telemetry: config.Telemetry{Enabled: false}})
	if count := bus.SubscriberCount(); count != before {
		t.Fatalf("turning it off left %d subscribers, was %d", count, before)
	}
	if _, err := os.Stat(filepath.Join(profile, "telemetry")); !os.IsNotExist(err) {
		t.Fatalf("the queue survived the switch going off: %v", err)
	}
}
