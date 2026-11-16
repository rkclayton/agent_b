package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"harness/internal/events"
	"harness/internal/modelinstall"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness/internal/config"
)

func TestWebModelStandIn(t *testing.T) {
	a := os.Args
	if len(a) < 3 || a[len(a)-2] != "--managed-model" {
		return
	}
	_ = http.ListenAndServe("127.0.0.1:"+a[len(a)-1], http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte("ok"))
			return
		}
		writeModelDone(w)
	}))
	os.Exit(0)
}
func managedWebFixture(t *testing.T) (agentConnectionFixture, modelinstall.Spec) {
	t.Helper()
	root := t.TempDir()
	original, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "fixture.exe")
	if err = os.WriteFile(exe, b, 0700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	f := newAgentConnectionFixture(t, url, url)
	s := modelinstall.Spec{ID: f.oldID, Root: root, Executable: exe, Args: []string{"-test.run=TestWebModelStandIn", "--", "--managed-model", fmt.Sprint(port)}, URL: url, Label: "Example model", IdleMinutes: 60, Files: []string{exe}, Bytes: int64(len(b))}
	if err = f.server.models.Configure(s); err != nil {
		t.Fatal(err)
	}
	return f, s
}
func TestManagedModelOnDemandKeepsMessageOnce(t *testing.T) {
	f, s := managedWebFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := f.server.bus.Listen(ctx, "managed test")
	if _, err := f.scheduler.Submit(ctx, f.session.ID, "one managed message"); err != nil {
		t.Fatal(err)
	}
	busy := false
	deadline := time.After(20 * time.Second)
	for !busy {
		select {
		case event := <-stream:
			if event.Type == events.ModelBusy && fmt.Sprint(event.Data.(map[string]any)["detail"]) == "loading model" {
				busy = true
			}
		case <-deadline:
			t.Fatal("loading model was not published")
		}
	}
	waitFor(t, func() bool { return !f.scheduler.Active(f.session.ID) }, "managed run did not finish")
	count := 0
	for _, m := range f.session.Snapshot().Messages {
		if m.Role == "user" && m.Content == "one managed message" {
			count++
		}
	}
	if count != 1 || f.server.models.State(s.ID).State != "running" {
		t.Fatalf("message count=%d state=%+v", count, f.server.models.State(s.ID))
	}
}
func TestManagedModelPublicPhoneDispatch(t *testing.T) {
	f, s := managedWebFixture(t)
	device := &recordingDevice{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() { f.server.streamToDevice(ctx, device); close(done) }()
	start := fmt.Sprintf(`{"v":1,"kind":"request","id":"model-start","route":"model.start","body":{"connection_id":%q}}`, s.ID)
	first := f.server.DispatchAppMessage("paired-fixture", []byte(start))
	if !bytes.Contains(first, []byte(`"status":200`)) {
		t.Fatalf("start: %s", first)
	}
	waitFor(t, func() bool { return f.server.models.State(s.ID).State == "running" }, "phone did not start the model")
	stop := dispatch(t, f.server, fmt.Sprintf(`{"v":1,"kind":"request","id":"model-stop","route":"model.stop","body":{"connection_id":%q}}`, s.ID))
	if stop.Status != 200 {
		t.Fatalf("stop: %s", stop.Body)
	}
	if !bytes.Equal(first, f.server.DispatchAppMessage("paired-fixture", []byte(start))) || f.server.models.State(s.ID).State != "stopped" {
		t.Fatal("replayed id restarted model")
	}
	device.waitFor(t, "model state", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		return data["type"] == "model.state"
	})
	bad := dispatch(t, f.server, fmt.Sprintf(`{"v":1,"kind":"request","id":"bad-model","route":"model.start","body":{"connection_id":%q,"command":"forbidden"}}`, s.ID))
	if bad.Status != 400 {
		t.Fatal("phone supplied command")
	}
	old := dispatch(t, f.server, `{"v":1,"kind":"request","id":"old-phone","route":"state","body":{}}`)
	if old.Status != 200 {
		t.Fatal("old phone state failed")
	}
	if err := f.server.models.Remove(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Executable); !os.IsNotExist(err) {
		t.Fatal("remove retained executable")
	}
}

func TestModelInstallMemoryUsesServerDetectionForBackend(t *testing.T) {
	server := &Server{cfg: &config.Config{Shell: config.Shell{ServiceAccount: config.ShellServiceAccount{Account: "fixture"}}}}
	server.detectLocal = func(context.Context, string) (any, error) {
		return map[string]any{
			"system_memory_bytes": uint64(32 << 30),
			"gpus": []any{
				map[string]any{"vendor": "Intel", "vram_bytes": uint64(2 << 30)},
				map[string]any{"vendor": "NVIDIA", "vram_bytes": uint64(4 << 30)},
			},
		}, nil
	}
	for backend, want := range map[string]uint64{"cpu": 32 << 30, "vulkan": 2 << 30, "cuda": 4 << 30} {
		got, err := server.modelInstallMemory(context.Background(), backend)
		if err != nil || got != want {
			t.Fatalf("%s got=%d err=%v want=%d", backend, got, err, want)
		}
	}
}

func TestManagedIdleRefusalPreservesSavedValue(t *testing.T) {
	f, _ := managedWebFixture(t)
	for _, value := range []any{5, -1, "invalid"} {
		cfg := f.server.ConfigSnapshot()
		b, _ := json.Marshal(cfg.Connections)
		var connections []map[string]any
		_ = json.Unmarshal(b, &connections)
		connections[0]["stop_when_idle_minutes"] = value
		body, _ := json.Marshal(map[string]any{"connections": connections})
		r := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(body))
		authorizeMutation(r, f.server)
		w := httptest.NewRecorder()
		f.server.Handler().ServeHTTP(w, r)
		if value == 5 && w.Code != 200 {
			t.Fatalf("save=%d %s", w.Code, w.Body)
		}
		if value != 5 && w.Code != 400 {
			t.Fatalf("invalid value accepted: %d %s", w.Code, w.Body)
		}
		saved := f.server.ConfigSnapshot().Connections[0].StopWhenIdleMinutes
		if saved == nil || *saved != 5 {
			t.Fatal("saved idle value changed")
		}
	}
}

func TestManagedRemoveLeavesProfilesAndRemovesConnection(t *testing.T) {
	f, s := managedWebFixture(t)
	s.ID = "installed-local"
	if err := f.server.models.Configure(s); err != nil {
		t.Fatal(err)
	}
	f.server.mu.Lock()
	f.server.cfg.Connections = f.server.cfg.Connections[:1]
	f.server.cfg.Connections[0].ID = s.ID
	f.server.mu.Unlock()
	r := httptest.NewRequest(http.MethodDelete, "/api/model-install?remove=installed-local", nil)
	authorizeMutation(r, f.server)
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("remove=%d %s", w.Code, w.Body)
	}
	saved, _, _, err := config.Load(f.server.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Connections) != 0 || len(saved.Agents) != 1 {
		t.Fatalf("connections=%d profiles=%d", len(saved.Connections), len(saved.Agents))
	}
	if _, err := os.Stat(s.Executable); !os.IsNotExist(err) {
		t.Fatal("owned executable remains")
	}
}
