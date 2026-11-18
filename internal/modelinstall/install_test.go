package modelinstall

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

type installRoundTrip func(*http.Request) (*http.Response, error)

func (f installRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInstallFailureLines2z0(t *testing.T) {
	for _, name := range []string{"prepare", "http_status", "network", "timeout", "disk_space", "hash_mismatch", "size", "unpack", "model_download", "metadata", "context", "server_find", "port", "server_start", "health", "startup", "manifest", "connection"} {
		t.Run(name, func(t *testing.T) {
			runtimeBytes, modelBytes := runtimeZip(t, "llama-server.exe"), ggufFixture(t, "fixture", 32768, 8, 2, 64, 64)
			if name == "unpack" {
				runtimeBytes = []byte("not a zip")
			}
			if name == "server_find" {
				runtimeBytes = runtimeZip(t, "other.txt")
			}
			if name == "metadata" {
				modelBytes = []byte("not a model")
			}
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if name == "http_status" || (name == "model_download" && r.URL.Path == "/model") {
					w.WriteHeader(503)
					return
				}
				if name == "timeout" {
					<-r.Context().Done()
					return
				}
				if r.URL.Path == "/model" {
					_, _ = w.Write(modelBytes)
				} else {
					_, _ = w.Write(runtimeBytes)
				}
			}))
			defer source.Close()
			artifact := Artifact{URL: source.URL + "/runtime", Name: "runtime.zip", Bytes: int64(len(runtimeBytes)), SHA256: digest(runtimeBytes)}
			if name == "hash_mismatch" {
				artifact.SHA256 = strings.Repeat("0", 64)
			}
			if name == "size" {
				artifact.Bytes++
			}
			root := t.TempDir()
			if name == "prepare" {
				if err := os.WriteFile(filepath.Join(root, "models"), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			client := source.Client()
			if name == "disk_space" {
				client.Transport = installRoundTrip(func(*http.Request) (*http.Response, error) { return nil, syscall.ENOSPC })
			}
			if name == "timeout" {
				client.Timeout = 20 * time.Millisecond
			}
			if name == "network" {
				source.Close()
			}
			m := New(root, "", client, Runtime{Version: "fixture", Backend: map[string][]Artifact{"cpu": {artifact}}}, []Model{{ID: "fixture", Artifact: Artifact{URL: source.URL + "/model", Name: "model.gguf", Bytes: int64(len(modelBytes)), SHA256: digest(modelBytes)}}}, func(context.Context, State) error { return errors.New("fixture registration refused") })
			defer m.Close()
			if name == "port" {
				m.port = func() (int, error) { return 0, syscall.EADDRINUSE }
			}
			m.healthTimeout = 30 * time.Millisecond
			if name == "startup" || name == "manifest" {
				file := "start-model.cmd"
				if name == "manifest" {
					file = "managed-model.json"
				}
				if err := os.MkdirAll(filepath.Join(root, "models", file), 0700); err != nil {
					t.Fatal(err)
				}
			}
			var health *http.Server
			m.start = func(_ string, args []string, _ string) (int, error) {
				if name != "connection" && name != "health" && name != "startup" && name != "manifest" {
					return 0, errors.New("fixture server refused")
				}
				port := ""
				for i := range args {
					if args[i] == "--port" {
						port = args[i+1]
					}
				}
				listener, err := net.Listen("tcp", "127.0.0.1:"+port)
				if err != nil {
					return 0, err
				}
				health = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if name == "health" {
						<-r.Context().Done()
						return
					}
					w.WriteHeader(200)
				})}
				go health.Serve(listener)
				return 0, nil
			}
			defer func() {
				if health != nil {
					_ = health.Close()
				}
			}()
			available := uint64(4 << 30)
			if name == "context" {
				available = 1
			}
			request := Request{ModelID: "fixture", Backend: "cpu", AvailableBytes: available}
			if err := m.Start(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			m.mu.RLock()
			done := m.done
			m.mu.RUnlock()
			if done != nil {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("attempt kept spinning")
				}
			}
			state := m.Snapshot()
			t.Logf("baseline case=%s running=%t phase=%s line=%s", name, state.Running, state.Phase, state.Error)
			if state.Running || state.Phase != "failed" || !strings.Contains(state.Error, "Retry") {
				t.Errorf("failure has no actionable retry line: %+v", state)
			}
			want := map[string][2]string{"prepare": {"prepare", "other"}, "http_status": {"runtime_download", "http_status"}, "network": {"runtime_download", "network"}, "timeout": {"runtime_download", "timeout"}, "disk_space": {"runtime_download", "disk_space"}, "hash_mismatch": {"runtime_download", "hash_mismatch"}, "size": {"runtime_download", "size"}, "unpack": {"runtime_unpack", "unpack"}, "model_download": {"model_download", "http_status"}, "metadata": {"metadata", "metadata"}, "context": {"context", "memory"}, "server_find": {"server_find", "other"}, "port": {"port", "port_in_use"}, "server_start": {"server_start", "server_start"}, "health": {"health", "timeout"}, "startup": {"startup", "other"}, "manifest": {"manifest", "other"}, "connection": {"connection", "other"}}[name]
			outcome := m.LastOutcome()
			if outcome.Step != want[0] || outcome.Error != want[1] || outcome.Outcome != "failed" {
				t.Errorf("outcome=%+v want=%v", outcome, want)
			}
			encoded, _ := json.Marshal(outcome)
			for _, secret := range []string{"fixture", "127.0.0.1", root, "http:", ".gguf", ".zip"} {
				if strings.Contains(string(encoded), secret) {
					t.Errorf("outcome disclosed %q", secret)
				}
			}
			logBytes, err := os.ReadFile(filepath.Join(root, "logs", "model-install.log"))
			if err != nil {
				t.Fatal(err)
			}
			if count := bytes.Count(logBytes, []byte(`"status":"failed"`)); count != 1 {
				t.Errorf("outcome records=%d", count)
			}
			if err := m.Start(context.Background(), request); err != nil {
				t.Fatalf("retry refused: %v", err)
			}
		})
	}
}

func TestInstallLogBoundAndPrivateOutcome2z0(t *testing.T) {
	m := New(t.TempDir(), "", nil, Runtime{}, nil, nil)
	download := (&url.URL{Scheme: "https", Host: "example.org", Path: "/file", RawQuery: "token=planted-token", User: url.UserPassword("someone", "planted-password")}).String()
	start := time.Now()
	for i := 0; i < installLogSlots+20; i++ {
		m.logStep("runtime_download", "completed", start, fmt.Errorf("download %s: dial tcp 192.168.1.10:443 token=planted-secret", download))
	}
	p := filepath.Join(m.root, "logs", "model-install.log")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != installLogHeader+installLogSlots*installLogSlotBytes {
		t.Fatalf("bound=%d", len(data))
	}
	for _, secret := range []string{"planted-password", "planted-token", "planted-secret", "192.168.1.10", "someone:", "https://"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("log contains %q", secret)
		}
	}
	minSeq := uint64(1 << 63)
	maxSeq := uint64(0)
	for at := installLogHeader; at < len(data); at += installLogSlotBytes {
		var row struct {
			Seq uint64 `json:"seq"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(data[at:at+installLogSlotBytes]), &row); err != nil {
			t.Fatal(err)
		}
		minSeq = min(minSeq, row.Seq)
		maxSeq = max(maxSeq, row.Seq)
	}
	if minSeq != 21 || maxSeq != 276 {
		t.Fatalf("retained sequence %d..%d", minSeq, maxSeq)
	}
	t.Logf("full bounded log=%d bytes, %d slots, oldest=%d newest=%d, elapsed=%s", len(data), installLogSlots, minSeq, maxSeq, time.Since(start))
	for _, backend := range []string{"cpu", "cuda", "vulkan"} {
		err := m.RejectMemory(Request{Backend: backend}, errors.New("fixture unavailable"))
		want := "gpu"
		if backend == "cpu" {
			want = "memory"
		}
		if err == nil || m.LastOutcome().Error != want || m.LastOutcome().Step != "preflight" {
			t.Fatal(m.LastOutcome())
		}
	}
}

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
	manager.mu.RLock()
	done := manager.done
	manager.mu.RUnlock()
	if done != nil {
		<-done
	}
	if outcome := manager.LastOutcome(); outcome.Outcome != "ok" || outcome.Step != "done" || outcome.Error != "" {
		t.Fatalf("success outcome=%+v", outcome)
	}
	if err := manager.Start(context.Background(), Request{ModelID: "missing", Backend: "cpu"}); err == nil {
		t.Fatal("catalog rejection absent")
	}
	logBytes, err := os.ReadFile(filepath.Join(root, "logs", "model-install.log"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(logBytes, []byte(`"status":"ok"`)) != 1 || bytes.Count(logBytes, []byte(`"status":"failed"`)) != 1 {
		t.Fatal("expected one ok and one failed attempt in log")
	}
	for _, line := range bytes.Split(logBytes, []byte{'\n'}) {
		if bytes.Contains(line, []byte(`"status":"ok"`)) || bytes.Contains(line, []byte(`"status":"failed"`)) {
			t.Logf("attempt log: %s", bytes.TrimSpace(line))
		}
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
	if spec, err := LoadSpec(filepath.Join(root, "models"), "", ""); err != nil || spec.IdleMinutes != 60 {
		t.Fatalf("fresh idle default=%d err=%v", spec.IdleMinutes, err)
	}
	if _, err := os.Stat(filepath.Join(startup, "Agent_b-model.vbs")); !os.IsNotExist(err) {
		t.Fatal("install wrote a Startup launcher")
	}
	for _, path := range []string{filepath.Join(root, "models", "fixture", "model.gguf"), filepath.Join(root, "models", "llama.cpp", "fixture", "cpu", "bin", "llama-server.exe")} {
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

func TestCancelStopsDownloadAndNeverRegistersPartialModel2xz(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	artifact := Artifact{URL: server.URL, Name: "runtime.zip", Bytes: 1048576, SHA256: strings.Repeat("0", 64)}
	manager := New(t.TempDir(), t.TempDir(), server.Client(), Runtime{Version: "fixture", Backend: map[string][]Artifact{"cpu": {artifact}}}, []Model{{ID: "fixture", Artifact: artifact}}, func(context.Context, State) error {
		t.Fatal("cancelled install registered a connection")
		return nil
	})
	if err := manager.Start(context.Background(), Request{ModelID: "fixture", Backend: "cpu", AvailableBytes: 4 << 30}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not begin")
	}
	state := manager.Cancel()
	if state.Running || state.Phase != "cancelled" {
		t.Fatalf("cancel state=%+v", state)
	}
	manager.Close()
	if outcome := manager.LastOutcome(); outcome.Outcome != "canceled" || outcome.Step != "runtime_download" || outcome.Error != "" {
		t.Fatalf("cancel outcome=%+v", outcome)
	}
	deadline := time.Now().Add(5 * time.Second)
	part := filepath.Join(manager.root, "models", "llama.cpp", "fixture", "cpu", "runtime.zip.part")
	for time.Now().Before(deadline) {
		if _, err := os.Stat(part); errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("partial download remained at cancellation boundary: %s", part)
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
