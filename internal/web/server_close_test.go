package web

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

// newTestServer is New with Close in the test's cleanup, which runs before the cleanup of
// any folder the test made earlier (item 2wr).
func newTestServer(t testing.TB, cfg *config.Config, path, webDir string, roots RuntimeRoots, bus *events.Bus) *Server {
	t.Helper()
	server := New(cfg, path, webDir, roots, bus)
	t.Cleanup(server.Close)
	return server
}

// A test server's folder can be removed the moment the test is done, and stays removed:
// Close stops the chat-tree watcher and the app loop first. Ubuntu CI saw "TempDir
// RemoveAll cleanup: directory not empty" when the watcher made its folder during the
// removal (item 2wr).
func TestAClosedServerLeavesItsFolderRemovable2wr(t *testing.T) {
	for attempt := range 50 {
		root, err := os.MkdirTemp("", "server-close")
		if err != nil {
			t.Fatal(err)
		}
		cfg := config.Defaults(root)
		bus := events.NewBus()
		server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, bus)
		writers, err := events.NewWriters(filepath.Join(root, "logs"))
		if err != nil {
			t.Fatal(err)
		}
		server.SetRegistry(session.NewRegistry(bus, writers, server.Connection, 0, server.ConfigSnapshot))
		server.Close()
		_ = writers.Close()
		if err := os.RemoveAll(root); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		time.Sleep(20 * time.Millisecond)
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			_ = os.RemoveAll(root)
			t.Fatalf("attempt %d: something wrote %s back after it was removed", attempt, filepath.Base(root))
		}
	}
}
