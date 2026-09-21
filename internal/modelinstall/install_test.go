package modelinstall

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func runtimeZip(t *testing.T, name string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	f, err := w.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("verified runtime fixture")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestVerifiedInstallDownloadsExtractsStartsAndRegisters(t *testing.T) {
	runtimeBytes, modelBytes := runtimeZip(t, "bin/llama-server.exe"), []byte("GGUF fixture")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/runtime" {
			_, _ = w.Write(runtimeBytes)
			return
		}
		_, _ = w.Write(modelBytes)
	}))
	defer server.Close()
	runtime := Runtime{Version: "fixture", Source: server.URL, Backend: map[string][]Artifact{"cpu": {{URL: server.URL + "/runtime", Name: "runtime.zip", Bytes: int64(len(runtimeBytes)), SHA256: digest(runtimeBytes)}}}}
	models := []Model{{ID: "fixture", Label: "Fixture", Source: server.URL, Artifact: Artifact{URL: server.URL + "/model", Name: "model.gguf", Bytes: int64(len(modelBytes)), SHA256: digest(modelBytes)}}}
	root, startup := t.TempDir(), t.TempDir()
	ready := make(chan State, 1)
	manager := New(root, startup, server.Client(), runtime, models, func(_ context.Context, state State) error { ready <- state; return nil })
	var health *http.Server
	manager.start = func(_ string, args []string, _ string) (int, error) {
		port, _ := strconv.Atoi(args[len(args)-1])
		health = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })}
		listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			return 0, err
		}
		go health.Serve(listener)
		return 321, nil
	}
	if err := manager.Start(context.Background(), Request{ModelID: "fixture", Backend: "cpu"}); err != nil {
		t.Fatal(err)
	}
	select {
	case state := <-ready:
		if state.ProfileID != "installed-local" || state.Phase != "ready" {
			t.Fatalf("state=%+v", state)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("install did not finish")
	}
	if health != nil {
		_ = health.Close()
	}
	for _, path := range []string{filepath.Join(root, "models", "fixture", "model.gguf"), filepath.Join(root, "models", "llama.cpp", "fixture", "cpu", "bin", "llama-server.exe"), filepath.Join(startup, "Agent_b-model.vbs")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
}

func TestTamperedDownloadIsRefusedBeforeExecution(t *testing.T) {
	data := []byte("tampered")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) }))
	defer server.Close()
	runtime := Runtime{Version: "fixture", Backend: map[string][]Artifact{"cpu": {{URL: server.URL, Name: "runtime.zip", Bytes: int64(len(data)), SHA256: digest([]byte("expected"))}}}}
	manager := New(t.TempDir(), t.TempDir(), server.Client(), runtime, []Model{{ID: "fixture", Artifact: Artifact{URL: server.URL, Name: "model.gguf", Bytes: int64(len(data)), SHA256: digest(data)}}}, nil)
	started := false
	manager.start = func(string, []string, string) (int, error) { started = true; return 1, nil }
	if err := manager.Start(context.Background(), Request{ModelID: "fixture", Backend: "cpu"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for manager.Snapshot().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if started || manager.Snapshot().Phase != "failed" {
		t.Fatalf("started=%v state=%+v", started, manager.Snapshot())
	}
}

func TestProductionDownloadsRequireHTTPSBeforeNetworkOrExecution(t *testing.T) {
	data := []byte("never requested")
	runtime := Runtime{Version: "fixture", Backend: map[string][]Artifact{"cpu": {{URL: "http://127.0.0.1/runtime", Name: "runtime.zip", Bytes: int64(len(data)), SHA256: digest(data)}}}}
	manager := New(t.TempDir(), t.TempDir(), nil, runtime, []Model{{ID: "fixture", Artifact: Artifact{URL: "https://example.invalid/model", Name: "model.gguf", Bytes: int64(len(data)), SHA256: digest(data)}}}, nil)
	started := false
	manager.start = func(string, []string, string) (int, error) { started = true; return 1, nil }
	if err := manager.Start(context.Background(), Request{ModelID: "fixture", Backend: "cpu"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for manager.Snapshot().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	state := manager.Snapshot()
	if started || state.Phase != "failed" || !strings.Contains(state.Error, "HTTPS is required") {
		t.Fatalf("started=%v state=%+v", started, state)
	}
}
