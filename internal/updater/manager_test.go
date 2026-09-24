package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckDownloadVerifyAndLaunch(t *testing.T) {
	setup := []byte("verified single-file setup")
	digest := sha256.Sum256(setup)
	manifest := map[string]any{
		"version": "v1.5.0", "commit": strings.Repeat("a", 40), "file": setupName,
		"sha256": hex.EncodeToString(digest[:]), "bytes": len(setup),
		"exe_identity": map[string]any{"tag": "v1.5.0", "commit": strings.Repeat("a", 40), "dirty": false},
	}
	var requests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.5.0", "body": "# First line\nmore", "assets": []map[string]string{{"name": manifestName, "browser_download_url": server.URL + "/release.json"}, {"name": setupName, "browser_download_url": server.URL + "/setup"}}})
		case "/release.json":
			_ = json.NewEncoder(w).Encode(manifest)
		case "/setup":
			_, _ = w.Write(setup)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var launched, reopened string
	expired := time.Now().Add(-24 * time.Hour)
	stampedWhileValid := expired.Add(-24 * time.Hour)
	manager := New(Options{CurrentVersion: "v1.4.0", DataRoot: t.TempDir(), LatestURL: server.URL + "/latest", Client: server.Client(), VerifySignature: func(context.Context, string) error {
		return acceptAuthenticode(authenticodeEvidence{Status: "Valid", Signer: true, Timestamped: true, SignerNotAfter: expired, TimestampTime: stampedWhileValid})
	}, Launch: func(path, sessionID string) error { launched, reopened = path, sessionID; return nil }})
	if err := manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := manager.State(); !state.Available || state.Version != "v1.5.0" || state.Notes != "First line" {
		t.Fatalf("unexpected state: %+v", state)
	}
	path, err := manager.Install(context.Background(), "same-chat")
	if err != nil {
		t.Fatal(err)
	}
	if path != launched {
		t.Fatalf("launched %q, returned %q", launched, path)
	}
	if reopened != "same-chat" {
		t.Fatalf("installer did not receive the selected chat: %q", reopened)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(setup) {
		t.Fatalf("verified setup: %q, %v", got, err)
	}
	if filepath.Base(path) != setupName || requests.Load() != 3 {
		t.Fatalf("path=%q requests=%d", path, requests.Load())
	}
}

func TestTamperedSetupIsRefusedBeforeLaunch(t *testing.T) {
	wanted := []byte("wanted")
	digest := sha256.Sum256(wanted)
	manifest := map[string]any{"version": "v1.5.0", "commit": strings.Repeat("b", 40), "file": setupName, "sha256": hex.EncodeToString(digest[:]), "bytes": len(wanted), "exe_identity": map[string]any{"tag": "v1.5.0", "commit": strings.Repeat("b", 40), "dirty": false}}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.5.0", "assets": []map[string]string{{"name": manifestName, "browser_download_url": server.URL + "/manifest"}, {"name": setupName, "browser_download_url": server.URL + "/setup"}}})
		case "/manifest":
			_ = json.NewEncoder(w).Encode(manifest)
		case "/setup":
			_, _ = w.Write([]byte("broken"))
		}
	}))
	defer server.Close()
	launched := false
	manager := New(Options{CurrentVersion: "v1.4.0", DataRoot: t.TempDir(), LatestURL: server.URL + "/latest", Client: server.Client(), Launch: func(string, string) error { launched = true; return nil }})
	if err := manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("error=%v", err)
	}
	if launched {
		t.Fatal("tampered setup was launched")
	}
}

func TestDisabledCheckMakesNoRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	manager := New(Options{CurrentVersion: "v1.4.0", DataRoot: t.TempDir(), LatestURL: server.URL, Client: server.Client(), Enabled: func() bool { return false }})
	if err := manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 || manager.State().Enabled {
		t.Fatalf("requests=%d state=%+v", requests.Load(), manager.State())
	}
}

func TestDisabledDuringCheckDiscardsLateRelease(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseRequest
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.5.0",
			"assets": []map[string]string{
				{"name": manifestName, "browser_download_url": server.URL + "/manifest"},
				{"name": setupName, "browser_download_url": server.URL + "/setup"},
			},
		})
	}))
	defer server.Close()
	enabled := atomic.Bool{}
	enabled.Store(true)
	manager := New(Options{CurrentVersion: "v1.4.0", DataRoot: t.TempDir(), LatestURL: server.URL, Client: server.Client(), Enabled: enabled.Load})
	done := make(chan error, 1)
	go func() { done <- manager.Check(context.Background()) }()
	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("update request did not start")
	}
	enabled.Store(false)
	manager.ConfigChanged(context.Background())
	close(releaseRequest)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if state := manager.State(); state.Enabled || state.Checking || state.Available || state.Version != "" {
		t.Fatalf("late release survived disabled switch: %+v", state)
	}
}

func TestWindowAttachCheckIsRateLimitedForFifteenMinutes(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.4.0"})
	}))
	defer server.Close()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	manager := New(Options{CurrentVersion: "v1.4.0", DataRoot: t.TempDir(), LatestURL: server.URL, Client: server.Client()})
	manager.now = func() time.Time { return now }
	if !manager.TriggerIfStale(context.Background(), 15*time.Minute) {
		t.Fatal("first attach did not start a check")
	}
	deadline := time.Now().Add(2 * time.Second)
	for manager.State().Checking && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if manager.TriggerIfStale(context.Background(), 15*time.Minute) {
		t.Fatal("second attach inside 15 minutes started a check")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests=%d, want 1", requests.Load())
	}
	now = now.Add(15 * time.Minute)
	if !manager.TriggerIfStale(context.Background(), 15*time.Minute) {
		t.Fatal("attach at 15 minutes did not start a check")
	}
	deadline = time.Now().Add(2 * time.Second)
	for requests.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests=%d, want 2", requests.Load())
	}
}

func TestHourlyLoopRetriesAFailedCheckAtTheNextTick(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.4.0"})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := New(Options{CurrentVersion: "v1.4.0", DataRoot: t.TempDir(), LatestURL: server.URL, Client: server.Client(), CheckInterval: 10 * time.Millisecond})
	manager.Start(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for (requests.Load() < 2 || manager.State().Error != "") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if requests.Load() < 2 || manager.State().Error != "" {
		t.Fatalf("failed check was not retried and cleared: requests=%d state=%+v", requests.Load(), manager.State())
	}
}
