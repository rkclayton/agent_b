package web

import (
	"context"
	"encoding/json"
	"sync"

	"harness/internal/buildinfo"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/telemetry"
)

// Item 2jg (d): off means off, and this file is where that is true or not.
//
// The switch does not filter. When telemetry is off there is NO SUBSCRIBER on
// the bus, so nothing is collected; the sender is not running, so nothing is
// batched; and the on-disk queue is deleted, so there is nothing to resume from.
// A test proves that by its absence — no subscriber, no queue directory, no
// batch handed to the transport — rather than by reading a flag back.
type telemetryState struct {
	sender      *telemetry.Sender
	unsubscribe func()
	// stop ends the reader. The bus does NOT close a channel on unsubscribe --
	// it only removes it from the fan-out map -- so a goroutine ranging over it
	// waits forever and takes the caller of Close with it. That deadlock was
	// real and this is the fix, not a precaution.
	stop chan struct{}
	done chan struct{}
}

type telemetryHost struct {
	mu    sync.Mutex
	state *telemetryState
	// transport is a test seam. In the product it is nil and the sender posts.
	transport func(body []byte) error
}

// applyTelemetry starts, stops or restarts collection to match the
// configuration. It is called at startup and whenever the switch or the
// endpoint changes.
func (s *Server) applyTelemetry(cfg config.Config) {
	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()
	s.stopTelemetryLocked()
	if !cfg.Telemetry.Enabled || cfg.Telemetry.Endpoint == "" {
		return
	}
	options := telemetry.Options{
		Endpoint:     cfg.Telemetry.Endpoint,
		InstallID:    cfg.Telemetry.InstallID,
		AgentVersion: buildinfo.Current().Tag,
		DataRoot:     s.profileRoot(),
	}
	if s.telemetry.transport != nil {
		forward := s.telemetry.transport
		options.Transport = func(_ context.Context, body []byte) error { return forward(body) }
	}
	sender := telemetry.New(options)
	if sender == nil {
		return
	}
	channel, unsubscribe := s.bus.Subscribe()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var event events.Event
			select {
			case <-stop:
				return
			case received, ok := <-channel:
				if !ok {
					return
				}
				event = received
			}
			data, _ := event.Data.(map[string]any)
			if data == nil && event.Data != nil {
				// A few events carry a struct. The wire form is what the filter
				// classifies, so it is normalized the same way here.
				encoded, err := json.Marshal(event.Data)
				if err != nil {
					continue
				}
				if err := json.Unmarshal(encoded, &data); err != nil {
					continue
				}
			}
			sender.Observe(event.Type, event.TS, data)
		}
	}()
	s.telemetry.state = &telemetryState{sender: sender, unsubscribe: unsubscribe, stop: stop, done: done}
}

func (s *Server) stopTelemetryLocked() {
	state := s.telemetry.state
	if state == nil {
		return
	}
	s.telemetry.state = nil
	state.unsubscribe()
	close(state.stop)
	<-state.done
	// Close deletes the queue. Off is not "sends less".
	state.sender.Close()
}

// ApplyTelemetry starts or stops collection from the current configuration. It
// is what startup and a profile switch call; the config route calls
// applyTelemetry directly with the configuration it just saved.
func (s *Server) ApplyTelemetry() { s.applyTelemetry(s.ConfigSnapshot()) }

// TelemetryRunning reports whether anything is collecting. It exists for the
// gate, which asks the question from outside rather than trusting the switch.
func (s *Server) TelemetryRunning() bool {
	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()
	return s.telemetry.state != nil
}


// NewInstallID is issued when the switch goes off then on, so two runs of
// telemetry from one machine cannot be joined.
func NewInstallID() string { return telemetry.NewInstallID() }

var _ = events.RunStopped
