package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"harness/internal/config"
)

type reachabilityTestTimer struct{ stopped bool }

func (timer *reachabilityTestTimer) Stop() bool {
	wasActive := !timer.stopped
	timer.stopped = true
	return wasActive
}

func TestReachabilityProbeBackoffIsBoundedAndStopsOnSuccess(t *testing.T) {
	server := &Server{reachability: map[string]*reachabilityRetry{}}
	var delays []time.Duration
	var timers []*reachabilityTestTimer
	server.reachabilityAfter = func(delay time.Duration, _ func()) operatorTimer {
		timer := &reachabilityTestTimer{}
		delays = append(delays, delay)
		timers = append(timers, timer)
		return timer
	}

	server.scheduleReachabilityProbe("local")
	server.scheduleReachabilityProbe("local")
	if len(delays) != 1 || delays[0] != time.Second {
		t.Fatalf("initial delays=%v", delays)
	}
	for index := 0; index < len(reachabilityBackoff)+2; index++ {
		server.cancelScheduledReachabilityProbe("local")
		server.completeReachabilityProbe("local", false)
	}
	if got := delays[len(delays)-1]; got != 30*time.Second {
		t.Fatalf("backoff did not cap at 30s: %v", delays)
	}
	server.completeReachabilityProbe("local", true)
	if _, ok := server.reachability["local"]; ok {
		t.Fatal("successful probe left retry state active")
	}
	if !timers[len(timers)-1].stopped {
		t.Fatal("successful probe did not stop pending retry")
	}
}

func TestReachabilityRetryUsesOnlyTheCompletionFreeHealthCheck2qw(t *testing.T) {
	fixture := newHealthFixture(t, 32768)
	server := newProbeServer(t)
	connection := runnableTestConnection("local")
	connection.BaseURL, connection.Model = fixture.server.URL, "beta"
	server.cfg.Connections = []config.Connection{connection}
	var callback func()
	server.reachabilityAfter = func(_ time.Duration, fn func()) operatorTimer {
		callback = fn
		return &reachabilityTestTimer{}
	}

	fixture.mode.Store("down")
	server.scheduleReachabilityProbe("local")
	callback()
	if server.connectionHealthState()["local"].Lamp != "alarm" || callback == nil {
		t.Fatalf("failed health retry did not remain scheduled: health=%+v", server.connectionHealthState()["local"])
	}
	fixture.mode.Store("up")
	callback()
	if server.connectionHealthState()["local"].Lamp != "ready" {
		t.Fatalf("successful health retry did not recover: health=%+v", server.connectionHealthState()["local"])
	}
	server.reachabilityMu.Lock()
	_, scheduled := server.reachability["local"]
	server.reachabilityMu.Unlock()
	if scheduled {
		t.Fatal("successful health retry left a retry scheduled")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, path := range fixture.paths {
		if path != "/v1/models" && path != "/props" {
			t.Fatalf("reachability retry sent %s", path)
		}
	}
}

// healthFixture is a model server that lists three models and reports a per-slot
// window, in a mode the test sets, and records every path it was asked for.
type healthFixture struct {
	server *httptest.Server
	mode   atomic.Value
	mu     sync.Mutex
	paths  []string
}

func newHealthFixture(t *testing.T, window int) *healthFixture {
	fixture := &healthFixture{}
	fixture.mode.Store("up")
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		fixture.paths = append(fixture.paths, r.URL.Path)
		fixture.mu.Unlock()
		switch fixture.mode.Load() {
		case "down":
			http.Error(w, "down", http.StatusServiceUnavailable)
		case "key":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		default:
			if r.URL.Path == "/props" {
				fmt.Fprintf(w, `{"default_generation_settings":{"n_ctx":%d}}`, window)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"alpha"},{"id":"beta"},{"id":"gamma"}]}`)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

// Item 2px CHECKS 5, 6, 7 and 10 (health): one state per connection from model-list
// requests only, following the server down and up within one check.
func TestConnectionHealthIsTheServersStateNow2px(t *testing.T) {
	fixture := newHealthFixture(t, 32768)
	server := newProbeServer(t)
	connection := runnableTestConnection("acme")
	connection.BaseURL, connection.Model = fixture.server.URL, "beta"
	for _, row := range []struct {
		name, model, mode string
		nctx              int
		lamp, word        string
	}{
		{"up with model", "beta", "up", 32768, "ready", "ready"},
		{"up without model", "", "up", 32768, "amber", "no model chosen"},
		{"model not listed", "delta", "up", 32768, "amber", "model not listed"},
		{"window smaller", "beta", "up", 60000, "amber", "server allows only 32768 tokens"},
		{"down", "beta", "down", 32768, "alarm", "unreachable"},
		{"key refused", "beta", "key", 32768, "alarm", "key refused"},
	} {
		fixture.mode.Store(row.mode)
		checked := connection
		checked.Model, checked.Context.NCtx = row.model, row.nctx
		server.checkConnectionHealth(context.Background(), checked)
		health := server.connectionHealthState()["acme"]
		t.Logf("2px health %-17s lamp=%-5s word=%q", row.name, health.Lamp, health.Word)
		if health.Lamp != row.lamp || health.Word != row.word {
			t.Errorf("%s: %+v, want %s %q", row.name, health, row.lamp, row.word)
		}
	}
	// CHECK 6 with a fast clock: a check every 20 ms stands in for every minute.
	fixture.mode.Store("up")
	server.cfg.Connections = []config.Connection{connection}
	server.healthEvery = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.StartConnectionHealth(ctx)
	waitFor := func(lamp string) {
		for deadline := time.Now().Add(2 * time.Second); server.connectionHealthState()["acme"].Lamp != lamp; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the lamp never went %s: %+v", lamp, server.connectionHealthState()["acme"])
			}
		}
	}
	waitFor("ready")
	fixture.mode.Store("down")
	waitFor("alarm")
	fixture.mode.Store("up")
	waitFor("ready")
	// CHECK 7: every request a check sent lists models or reads the window.
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, path := range fixture.paths {
		if path != "/v1/models" && path != "/props" {
			t.Fatalf("a health check sent %s", path)
		}
	}
	t.Logf("2px health checks sent %d requests, all /v1/models or /props", len(fixture.paths))
}

func TestHealthCheckLeavesAnEmptyContextEmpty2sx(t *testing.T) {
	fixture := newHealthFixture(t, 49152)
	server := newProbeServer(t)
	connection := runnableTestConnection("local")
	connection.BaseURL, connection.Model, connection.Context.NCtx = fixture.server.URL, "beta", 0
	server.cfg.Connections = []config.Connection{connection}
	server.checkConnectionHealth(context.Background(), connection)
	if got := server.ConfigSnapshot().Connections[0].Context.NCtx; got != 0 {
		t.Fatalf("health check wrote the server window into saved context: %d", got)
	}
	server.cfg.Connections[0].Context.NCtx = 65536
	for index := 0; index < 10; index++ {
		server.checkConnectionHealth(context.Background(), server.ConfigSnapshot().Connections[0])
	}
	if got := server.ConfigSnapshot().Connections[0].Context.NCtx; got != 65536 {
		t.Fatalf("typed context replaced after ten checks: %d", got)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, path := range fixture.paths {
		if path != "/v1/models" && path != "/props" {
			t.Fatalf("health check sent completion path %s", path)
		}
	}
	unknown := newHealthFixture(t, 0)
	server.cfg.Connections[0].BaseURL, server.cfg.Connections[0].Context.NCtx = unknown.server.URL, 0
	server.checkConnectionHealth(context.Background(), server.ConfigSnapshot().Connections[0])
	if got := server.ConfigSnapshot().Connections[0].Context.NCtx; got != 0 {
		t.Fatalf("server publishing no window filled context with %d", got)
	}
}

func TestBackgroundHealthNeverWritesRecognizedServerConnections2sx(t *testing.T) {
	fixture := newHealthFixture(t, 32768)
	server := newProbeServer(t)
	server.cfg.Connections = nil
	for _, kind := range []string{"llama.cpp", "vllm", "ollama", "unknown"} {
		connection := runnableTestConnection(kind)
		connection.BaseURL, connection.Model, connection.Context.NCtx = fixture.server.URL, "beta", 0
		connection.Capabilities.Server = kind
		server.cfg.Connections = append(server.cfg.Connections, connection)
	}
	before, _ := json.Marshal(server.ConfigSnapshot())
	server.healthEvery = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	server.StartConnectionHealth(ctx)
	time.Sleep(30 * time.Millisecond)
	cancel()
	after, _ := json.Marshal(server.ConfigSnapshot())
	if string(before) != string(after) {
		t.Fatal("background health changed saved connection bytes")
	}
}

// 2qw: Test requires a chosen listed model; the independent picker supplies it.
func TestTestWithNoModelNamesTheRequiredAction2qw(t *testing.T) {
	fixture := newHealthFixture(t, 32768)
	server := newProbeServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api/connections/local/probe", strings.NewReader(`{"base_url":"`+fixture.server.URL+`"}`))
	response := httptest.NewRecorder()
	server.connection(response, request)
	var answer struct {
		Status, Message, Error string
		Models                 []string
	}
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil || answer.Status != "failed" || answer.Message != "Test failed — choose a listed model and try again" {
		t.Fatalf("Test without a model: %d %s", response.Code, response.Body)
	}
	// A model can still be saved without Test, and Save starts no capability probe.
	if saved := postConfigPatch(t, server, `{"connections":[{"id":"local","base_url":"`+fixture.server.URL+`","model":"beta"}]}`); saved.Code != http.StatusOK || server.ConfigSnapshot().Connections[0].Model != "beta" {
		t.Fatalf("saving a listed model: %d %.200s", saved.Code, saved.Body)
	}
}

// CHECK 8: a reserve derived from an old window follows the new one; Save succeeds.
func TestTheReserveFollowsTheWindow2px(t *testing.T) {
	server := newProbeServer(t)
	server.cfg.Connections[0] = runnableTestConnection("acme")
	server.cfg.Connections[0].Context.NCtx, server.cfg.Connections[0].Capabilities.NCtx = 60480, 0
	server.cfg.Connections[0].Context.ReserveOutput = 30240
	server.cfg.Agents = []config.Agent{{Name: "acme", B: "acme", Toolset: config.FullToolset()}}
	for _, patch := range []string{`{"connections":[{"id":"acme","context":{"n_ctx":60000}}]}`, `{"connections":[{"id":"acme","context":{"n_ctx":60000,"reserve_output":30240}}]}`} {
		response := postConfigPatch(t, server, patch)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "connections[") || server.ConfigSnapshot().Connections[0].Context.ReserveOutput != 30000 {
			t.Fatalf("%s: %d reserve=%d %.200s", patch, response.Code, server.ConfigSnapshot().Connections[0].Context.ReserveOutput, response.Body)
		}
	}
}
