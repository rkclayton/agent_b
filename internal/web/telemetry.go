package web

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"harness/internal/buildinfo"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/recorder"
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
	// shapeSent is the last settings.shape sent (item 2q6 (g)).
	shapeSent string
}

// applyTelemetry starts, stops or restarts collection to match the
// configuration. It is called at startup and whenever the switch or the
// endpoint changes.
func (s *Server) applyTelemetry(cfg config.Config) {
	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()
	s.stopTelemetryLocked()
	if !cfg.Telemetry.Enabled {
		return
	}
	endpoint := cfg.Telemetry.Endpoint
	if endpoint == "" {
		endpoint = telemetry.DefaultEndpoint
	}
	options := telemetry.Options{
		Endpoint:     endpoint,
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
			if !telemetry.RecorderTypes[event.Type] {
				sender.Observe(event.Type, event.TS, data)
			}
		}
	}()
	// Item 2q6 (g): the settings' shape when collection starts — which is every
	// configuration save — if it changed, and once a day regardless.
	s.queueSettingsShapeLocked(sender, cfg)
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				current := s.ConfigSnapshot()
				s.telemetry.mu.Lock()
				if s.telemetry.state != nil && s.telemetry.state.sender == sender {
					s.telemetry.shapeSent = ""
					s.queueSettingsShapeLocked(sender, current)
				}
				s.telemetry.mu.Unlock()
			}
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

func (s *Server) QueueStartupTelemetry(eventType, at string, data map[string]any) bool {
	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()
	if s.telemetry.state == nil || !s.telemetry.state.sender.Observe(eventType, at, data) {
		return false
	}
	s.telemetry.state.sender.Flush()
	return true
}

// queueRunTelemetry is the recorder's sink (item 2pw (d)): with the switch off
// there is no sender, and the event goes nowhere.
func (s *Server) queueRunTelemetry(eventType string, data map[string]any) {
	s.telemetry.mu.Lock()
	state := s.telemetry.state
	s.telemetry.mu.Unlock()
	if state != nil {
		state.sender.Observe(eventType, time.Now().UTC().Format(time.RFC3339), data)
	}
}

// settingsShape is item 2q6 (g): per connection its kind, model file name,
// window, reserve, reasoning effort; the context thresholds; the switch; the OS
// version. No address, key, name, path or label.
func settingsShape(cfg config.Config) map[string]any {
	connections := []any{}
	for _, connection := range cfg.Connections {
		kind := "api"
		if parsed, err := url.Parse(connection.BaseURL); err == nil && (parsed.Hostname() == "localhost" || net.ParseIP(parsed.Hostname()).IsLoopback()) {
			kind = "local"
		}
		connections = append(connections, map[string]any{"kind": kind, "model": telemetry.Redact(filepath.Base(filepath.ToSlash(connection.Model))),
			"context_size": connection.Context.NCtx, "reserve": connection.Context.ReserveOutput, "reasoning_effort": telemetry.Redact(connection.Reasoning.Effort),
			"soft_pct": cfg.Context.SoftPct, "summary_pct": cfg.Context.SummaryPct})
	}
	return map[string]any{"connections": connections, "telemetry": cfg.Telemetry.Enabled, "os_version": osVersion()}
}

// queueSettingsShapeLocked sends the shape when it differs from the last one
// sent. The caller holds s.telemetry.mu.
func (s *Server) queueSettingsShapeLocked(sender *telemetry.Sender, cfg config.Config) {
	shape := settingsShape(cfg)
	encoded, _ := json.Marshal(shape)
	if s.telemetry.shapeSent == string(encoded) {
		return
	}
	s.telemetry.shapeSent = string(encoded)
	sender.Observe(events.SettingsShape, time.Now().UTC().Format(time.RFC3339), shape)
}

// reportChat is item 2pw (c): the chat's last runs as one trace event, sent now
// whether or not the switch is on — the click is the consent for that report.
func (s *Server) reportChat(w http.ResponseWriter, id string) {
	trace, ok := s.recorder.Trace(id)
	if !ok {
		writeError(w, http.StatusNotFound, "nothing is recorded for this chat yet", "session")
		return
	}
	cfg := s.ConfigSnapshot()
	options := telemetry.Options{Endpoint: cfg.Telemetry.Endpoint, AgentVersion: buildinfo.Current().Tag}
	s.telemetry.mu.Lock()
	if forward := s.telemetry.transport; forward != nil {
		options.Transport = func(_ context.Context, body []byte) error { return forward(body) }
	}
	s.telemetry.mu.Unlock()
	if _, err := telemetry.ReportOne(options, events.Trace, trace); err != nil {
		writeError(w, http.StatusBadGateway, "the report was not accepted: "+telemetry.Redact(err.Error()), "report")
		return
	}
	s.app.NoteReport()
	writeJSON(w, http.StatusOK, map[string]any{"report_id": trace["report_id"]})
}

// pageHealth is item 2q7 (b) and (f) from the page: its longest freeze and the
// settings pages it opened. Names are checked against a word pattern there.
func (s *Server) pageHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var body struct {
		FreezeMS int            `json:"freeze_ms"`
		Pages    map[string]int `json:"settings_pages"`
	}
	if !decode(w, r, &body) {
		return
	}
	s.app.NotePage(min(max(body.FreezeMS, 0), 600000), body.Pages)
	w.WriteHeader(http.StatusNoContent)
}

// resourceProbe is item 2q7 (e), run once an hour by the recorder: the data
// folder's and the chat store's size, the chat count, RAM and the OS build.
func (s *Server) resourceProbe() map[string]any {
	out := map[string]any{"os_version": osVersion()}
	walk := func(root string) (bytes, files int64) {
		_ = filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
				if info, err := entry.Info(); err == nil {
					bytes, files = bytes+info.Size(), files+1
				}
			}
			return nil
		})
		return bytes, files
	}
	if root := s.profileRoot(); root != "" {
		out["data_bytes"], _ = walk(root)
		out["chats_bytes"], _ = walk(filepath.Join(root, "chats"))
	}
	s.chatMu.Lock()
	out["chats"] = int64(len(s.chatEntries))
	s.chatMu.Unlock()
	if ram := totalRAM(); ram > 0 {
		out["ram_bytes"] = ram
	}
	return out
}

// NewInstallID is issued when the switch goes off then on, so two runs of
// telemetry from one machine cannot be joined.
func NewInstallID() string { return telemetry.NewInstallID() }

var _ = events.RunStopped

// App is the app-health aggregator (item 2q7), for start-up's notes.
func (s *Server) App() *recorder.App { return s.app }
