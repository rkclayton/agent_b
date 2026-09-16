package tools

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/session"
)

func TestRecursiveFileToolsSkipInaccessibleSubdirectories(t *testing.T) {
	root := t.TempDir()
	denied := filepath.Join(root, "denied")
	if err := os.Mkdir(denied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("needle visible\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(denied, "hidden.txt"), []byte("needle hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	item := &session.Session{Workspace: root, LastSeen: map[string]time.Time{}}
	denyDuringWalk := func(root string, callback fs.WalkDirFunc) error {
		return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if path == denied {
				if callbackErr := callback(path, entry, fs.ErrPermission); callbackErr != nil && callbackErr != filepath.SkipDir {
					return callbackErr
				}
				return filepath.SkipDir
			}
			return callback(path, entry, err)
		})
	}

	t.Run("find_files", func(t *testing.T) {
		tool := NewGlob(config.FindFilesTool{})
		tool.walkDir = denyDuringWalk
		result, err := tool.Call(context.Background(), item, map[string]any{"pattern": "*.txt"})
		assertSkippedResult(t, result, err, "visible.txt", "hidden.txt")
	})
	t.Run("search_text", func(t *testing.T) {
		defaults := config.Defaults(root)
		tool := NewGrep(defaults.Tools.Grep, defaults.Tools.ListDir)
		tool.walkDir = denyDuringWalk
		result, err := tool.Call(context.Background(), item, map[string]any{"pattern": "needle"})
		assertSkippedResult(t, result, err, "visible.txt:1", "hidden.txt")
	})
	t.Run("list_dir", func(t *testing.T) {
		tool := NewListDir(config.Defaults(root).Tools.ListDir)
		readDir := tool.readDir
		tool.readDir = func(path string) ([]os.DirEntry, error) {
			if path == denied {
				return nil, fs.ErrPermission
			}
			return readDir(path)
		}
		result, err := tool.Call(context.Background(), item, map[string]any{"depth": 2})
		assertSkippedResult(t, result, err, "visible.txt", "hidden.txt")
	})
}

func assertSkippedResult(t *testing.T, result string, err error, visible, hidden string) {
	t.Helper()
	if err != nil || !strings.Contains(result, visible) || strings.Contains(result, hidden) || !strings.Contains(result, "[skipped 1 inaccessible]") {
		t.Fatalf("result=%q err=%v", result, err)
	}
}
