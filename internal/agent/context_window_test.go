package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"harness/internal/config"
)

func TestResolveContextWindowOrderNeverReturnsZero(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":70000},"n_ctx":60000}`))
	}))
	defer server.Close()
	base := config.Profile{ID: "fresh", Label: "Fresh model", BaseURL: server.URL, Context: config.Context{ReserveOutput: 10240}}

	profile := base
	profile.Context.NCtx = 90000
	profile.Capabilities.NCtx = 80000
	resolved, source, err := resolveContextWindow(context.Background(), &profile)
	if err != nil || resolved.Context.NCtx != 90000 || source != "profile context size" || requests.Load() != 0 {
		t.Fatalf("profile resolution = n_ctx %d source %q requests %d err %v", resolved.Context.NCtx, source, requests.Load(), err)
	}

	profile = base
	profile.Capabilities.NCtx = 80000
	resolved, source, err = resolveContextWindow(context.Background(), &profile)
	if err != nil || resolved.Context.NCtx != 80000 || source != "probed n_ctx" || requests.Load() != 0 {
		t.Fatalf("probe resolution = n_ctx %d source %q requests %d err %v", resolved.Context.NCtx, source, requests.Load(), err)
	}

	profile = base
	resolved, source, err = resolveContextWindow(context.Background(), &profile)
	if err != nil || resolved.Context.NCtx != 70000 || source != "per-slot /props n_ctx" || requests.Load() != 1 {
		t.Fatalf("props resolution = n_ctx %d source %q requests %d err %v", resolved.Context.NCtx, source, requests.Load(), err)
	}
}

func TestResolveContextWindowRefusesUnknownByProfileName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	profile := config.Profile{ID: "fresh", Label: "My model", BaseURL: server.URL}
	resolved, _, err := resolveContextWindow(context.Background(), &profile)
	if resolved != nil || err == nil || !strings.Contains(err.Error(), `profile "My model" context size unknown`) {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
}
