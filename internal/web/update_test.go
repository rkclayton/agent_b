package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/updater"
)

func TestWindowAttachEndpointTriggersOneRateLimitedCheck(t *testing.T) {
	var requests atomic.Int32
	release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.6.5"})
	}))
	defer release.Close()
	root := t.TempDir()
	cfg := config.Defaults(root)
	server := New(&cfg, root+`\harness.json`, root, RuntimeRoots{Application: root, Data: root, Workspace: root}, events.NewBus())
	manager := updater.New(updater.Options{CurrentVersion: "v1.6.5", DataRoot: root, LatestURL: release.URL, Client: release.Client()})
	server.SetUpdater(manager)

	for index := 0; index < 2; index++ {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/update?attach=1", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("attach %d status=%d body=%s", index+1, response.Code, response.Body)
		}
		deadline := time.Now().Add(2 * time.Second)
		for manager.State().Checking && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("attach checks=%d, want 1", requests.Load())
	}
}
