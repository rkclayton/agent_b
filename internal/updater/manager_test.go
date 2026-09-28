package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	// Item 2mk (b): FOUR requests now, not three. The check asks for the latest
	// release and then, only because one is available, for the list beside it, so it
	// can say how many releases behind this build is. The download and the manifest
	// are the other two.
	if filepath.Base(path) != setupName || requests.Load() != 4 {
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

// Item 2nh (a): THE UPDATE IS A SEQUENCE OF STAGES, PUBLISHED AS IT RUNS.
//
// "update failed. this needs to be a more controlled process with a loading bar
// and error handling that's evident" — written about an update that SUCCEEDED. The
// control said "Downloading and verifying…" from the first moment to the last and
// then the window closed, so every stage looked like the same stage. These are the
// states a watching page would have been given, in order, and the download's are
// determinate because the release manifest names the size.
func TestTheUpdateIsASequenceOfStages2nh(t *testing.T) {
	setup := bytes.Repeat([]byte("s"), 700*1024)
	digest := sha256.Sum256(setup)
	manifest := map[string]any{
		"version": "v1.31.0", "commit": strings.Repeat("b", 40), "file": setupName,
		"sha256": hex.EncodeToString(digest[:]), "bytes": len(setup),
		"exe_identity": map[string]any{"tag": "v1.31.0", "commit": strings.Repeat("b", 40), "dirty": false},
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.31.0", "body": "notes", "assets": []map[string]string{{"name": manifestName, "browser_download_url": server.URL + "/release.json"}, {"name": setupName, "browser_download_url": server.URL + "/setup"}}})
		case "/release.json":
			_ = json.NewEncoder(w).Encode(manifest)
		case "/setup":
			_, _ = w.Write(setup)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var mu sync.Mutex
	stages := []string{}
	determinate := int64(0)
	manager := New(Options{CurrentVersion: "v1.30.0", DataRoot: t.TempDir(), LatestURL: server.URL + "/latest", Client: server.Client(),
		VerifySignature: func(context.Context, string) error { return nil },
		Launch:          func(string, string) error { return nil },
		Changed: func(state State) {
			mu.Lock()
			defer mu.Unlock()
			if state.Step == "" {
				return
			}
			if state.Line == "" {
				t.Errorf("stage %q published no line, and a wait element without one is decoration", state.Step)
			}
			if state.Step == "downloading" && state.Total > 0 && state.Processed > determinate {
				determinate = state.Processed
			}
			if len(stages) == 0 || stages[len(stages)-1] != state.Step {
				stages = append(stages, state.Step)
			}
		}})
	if err := manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"downloading", "verifying", "installing"}
	if len(stages) != len(want) {
		t.Fatalf("stages %q, want %q", stages, want)
	}
	for index, stage := range want {
		if stages[index] != stage {
			t.Fatalf("stages %q, want %q", stages, want)
		}
	}
	if determinate != int64(len(setup)) {
		t.Fatalf("the download reported %d of %d bytes; the last chunk must be reported too", determinate, len(setup))
	}
}

// Item 2mk (b) and (c): HOW FAR BEHIND, AND WHICH INSTALL.
//
// "yeah thats fine i just used the in app updater" — said while eleven releases
// behind, a number he got from a report and not from the product. One published
// release is one thing he did not get, so this counts releases and not the
// difference between two version numbers, which would have said "1".
func TestTheCheckSaysHowManyReleasesBehindAndWhichInstall2mk(t *testing.T) {
	setup := []byte("setup")
	digest := sha256.Sum256(setup)
	manifest := map[string]any{
		"version": "v1.24.0", "commit": strings.Repeat("c", 40), "file": setupName,
		"sha256": hex.EncodeToString(digest[:]), "bytes": len(setup),
		"exe_identity": map[string]any{"tag": "v1.24.0", "commit": strings.Repeat("c", 40), "dirty": false},
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.24.0", "body": "notes", "assets": []map[string]string{{"name": manifestName, "browser_download_url": server.URL + "/release.json"}, {"name": setupName, "browser_download_url": server.URL + "/setup"}}})
		case r.URL.Path == "/releases":
			// Eleven stable releases newer than v1.13.0, plus two that must not
			// count: a draft and a prerelease.
			list := []map[string]any{{"tag_name": "v1.25.0", "draft": true}, {"tag_name": "v1.24.1", "prerelease": true}, {"tag_name": "v1.13.0"}, {"tag_name": "v1.12.0"}}
			for _, tag := range []string{"v1.24.0", "v1.23.0", "v1.22.0", "v1.21.0", "v1.20.0", "v1.19.0", "v1.18.0", "v1.17.0", "v1.16.0", "v1.15.0", "v1.14.0"} {
				list = append(list, map[string]any{"tag_name": tag})
			}
			_ = json.NewEncoder(w).Encode(list)
		case r.URL.Path == "/release.json":
			_ = json.NewEncoder(w).Encode(manifest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	manager := New(Options{CurrentVersion: "v1.13.0", DataRoot: root, ApplicationRoot: `C:\Users\Operator\AppData\Local\Programs\Agent_b`,
		LatestURL: server.URL + "/releases/latest", Client: server.Client(),
		VerifySignature: func(context.Context, string) error { return nil },
		Launch:          func(string, string) error { return nil }})
	if err := manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := manager.State()
	if !state.Available || state.ReleasesBehind != 11 {
		t.Fatalf("available=%v behind=%d, want 11 releases behind", state.Available, state.ReleasesBehind)
	}
	if state.ApplicationRoot == "" {
		t.Fatal("the state does not name the install it is about, which is (c)")
	}
	// (d): an updater switched off says so, and takes its count with it — "update
	// checks are off · 11 releases behind" would be a number nothing is refreshing.
	on := true
	off := New(Options{CurrentVersion: "v1.13.0", DataRoot: t.TempDir(), LatestURL: server.URL + "/releases/latest", Client: server.Client(),
		Enabled:         func() bool { return on },
		VerifySignature: func(context.Context, string) error { return nil },
		Launch:          func(string, string) error { return nil }})
	if err := off.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := off.State(); !state.Available || state.ReleasesBehind != 11 {
		t.Fatalf("before switching off: available=%v behind=%d", state.Available, state.ReleasesBehind)
	}
	on = false
	off.refreshEnabled()
	if state := off.State(); state.Enabled || state.Available || state.ReleasesBehind != 0 {
		t.Fatalf("a switched-off updater kept %+v", state)
	}
}

// Item 2mk (a): an install that finished while this window went on serving the old
// build is reported as exactly that.
func TestAnInstallThisWindowDidNotBecomeIsReported2mk(t *testing.T) {
	root := t.TempDir()
	writePhases(t, root,
		progressPhase{Phase: "starting", Text: "Installing Agent_b v1.22.0"},
		progressPhase{At: "2026-09-27T04:32:07Z", Phase: "finished", Text: "Agent_b v1.22.0 is installed.", Done: true, OK: true},
	)
	manager := New(Options{CurrentVersion: "v1.13.0", DataRoot: root, ApplicationRoot: `C:\Users\Operator\AppData\Local\Programs\Agent_b`})
	outcome := manager.State().Outcome
	if outcome == nil || !outcome.OK || outcome.Running != "v1.13.0" || outcome.Version != "v1.22.0" {
		t.Fatalf("outcome %+v, want v1.22.0 installed while v1.13.0 runs", outcome)
	}
	if outcome.ApplicationRoot == "" {
		t.Fatal("the outcome does not name the install it is about")
	}
}
