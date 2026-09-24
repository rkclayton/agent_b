package modelinstall

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
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

func ggufFixture(t *testing.T, arch string, contextLength, blocks, headsKV, keyLength, valueLength uint64) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("GGUF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(3))
	_ = binary.Write(&b, binary.LittleEndian, uint64(0))
	_ = binary.Write(&b, binary.LittleEndian, uint64(6))
	writeString := func(value string) {
		_ = binary.Write(&b, binary.LittleEndian, uint64(len(value)))
		b.WriteString(value)
	}
	writeString("general.architecture")
	_ = binary.Write(&b, binary.LittleEndian, uint32(8))
	writeString(arch)
	for key, value := range map[string]uint64{
		arch + ".context_length":          contextLength,
		arch + ".block_count":             blocks,
		arch + ".attention.head_count_kv": headsKV,
		arch + ".attention.key_length":    keyLength,
		arch + ".attention.value_length":  valueLength,
	} {
		writeString(key)
		_ = binary.Write(&b, binary.LittleEndian, uint32(10))
		_ = binary.Write(&b, binary.LittleEndian, value)
	}
	return b.Bytes()
}

func TestVerifiedInstallDownloadsExtractsStartsAndRegisters(t *testing.T) {
	runtimeBytes, modelBytes := runtimeZip(t, "bin/llama-server.exe"), ggufFixture(t, "fixture", 32768, 8, 2, 64, 64)
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
	var startArgs []string
	manager.start = func(_ string, args []string, _ string) (int, error) {
		startArgs = append([]string(nil), args...)
		port := 0
		for index := range args {
			if args[index] == "--port" && index+1 < len(args) {
				port, _ = strconv.Atoi(args[index+1])
			}
		}
		health = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })}
		listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			return 0, err
		}
		go health.Serve(listener)
		return 321, nil
	}
	if err := manager.Start(context.Background(), Request{ModelID: "fixture", Backend: "cpu", AvailableBytes: 4 << 30}); err != nil {
		t.Fatal(err)
	}
	select {
	case state := <-ready:
		if state.ConnectionID != "installed-local" || state.Phase != "ready" {
			t.Fatalf("state=%+v", state)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("install did not finish")
	}
	if health != nil {
		_ = health.Close()
	}
	if state := manager.Snapshot(); state.Context == nil || state.Context.NCtx != 32768 {
		t.Fatalf("context sizing=%+v", state.Context)
	}
	if joined := strings.Join(startArgs, " "); !strings.Contains(joined, "--ctx-size 32768") {
		t.Fatalf("launch args=%q", startArgs)
	}
	if _, err := os.Stat(filepath.Join(root, "models", "fixture", "context-sizing.json")); err != nil {
		t.Fatalf("context sizing record: %v", err)
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
	if err := manager.Start(context.Background(), Request{ModelID: "fixture", Backend: "cpu", AvailableBytes: 4 << 30}); err != nil {
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
	if err := manager.Start(context.Background(), Request{ModelID: "fixture", Backend: "cpu", AvailableBytes: 4 << 30}); err != nil {
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

func TestContextSizingSixCases(t *testing.T) {
	type shape struct {
		name     string
		weights  int64
		metadata GGUFMetadata
	}
	shapes := []shape{
		{"0.8B", 579615840, GGUFMetadata{ContextLength: 262144, BlockCount: 25, HeadCountKV: 2, KeyLength: 256, ValueLength: 256}},
		{"8B", 2 << 30, GGUFMetadata{ContextLength: 131072, BlockCount: 32, HeadCountKV: 8, KeyLength: 128, ValueLength: 128}},
		{"14B", 3 << 30, GGUFMetadata{ContextLength: 131072, BlockCount: 40, HeadCountKV: 8, KeyLength: 128, ValueLength: 128}},
	}
	expected := map[string]int{"0.8B/4": 49152, "0.8B/16": 262144, "8B/4": 8192, "8B/16": 106496, "14B/16": 77824}
	refusals := 0
	for _, current := range shapes {
		for _, gib := range []uint64{4, 16} {
			name := fmt.Sprintf("%s/%d", current.name, gib)
			t.Run(name, func(t *testing.T) {
				got, err := computeContext(current.weights, gib<<30, current.metadata)
				want, fits := expected[name]
				if !fits {
					if err == nil || !strings.Contains(err.Error(), "cannot fit context floor") {
						t.Fatalf("got=%+v err=%v", got, err)
					}
					refusals++
					return
				}
				if err != nil || got.NCtx != want {
					t.Fatalf("got=%+v err=%v want=%d", got, err, want)
				}
			})
		}
	}
	if refusals != 1 {
		t.Fatalf("refusals=%d, want 1", refusals)
	}
}
