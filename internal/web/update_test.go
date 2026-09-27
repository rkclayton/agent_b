package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// Item 2mr (c): the reason reaches the control. The operator pressed Update, saw
// "downloading and verifying", and was returned to "available" with nothing said —
// because the error branch of the About status line sat BELOW the available branch
// and a failed install leaves the update available. This asserts the order, in the
// shipped source, because the defect was entirely an order of branches.
func TestTheUpdateLineShowsWhyAnInstallFailed(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "web", "js", "settings-about.js"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	failure := strings.Index(text, "failure ? failure")
	available := strings.Index(text, "update.available ?")
	if failure < 0 || available < 0 {
		t.Fatal("the About status line no longer has an available branch and a failure branch")
	}
	if failure > available {
		t.Fatal("the failure branch must be reached before the available branch, or a failed install shows no reason")
	}
	if !strings.Contains(text, "update failed · ${update.error}") {
		t.Fatal("a failed install must name the updater's own reason")
	}
	// And it must still say which version is on offer, so the reason does not cost
	// the reader the fact the line used to carry.
	if !strings.Contains(text, "${update.version} available · update failed") {
		t.Fatal("the failed line must still name the available version")
	}
}
