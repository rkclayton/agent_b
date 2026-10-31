package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/session"
)

func TestGlob(t *testing.T) {
	t.Run("match_and_sort", func(t *testing.T) {
		root := t.TempDir()
		writeGlobFixture(t, root, "z.go")
		writeGlobFixture(t, root, "nested/b.go")
		writeGlobFixture(t, root, "nested/a.go")
		writeGlobFixture(t, root, "nested/no.txt")
		writeGlobFixture(t, root, "logs/hidden.go")
		writeGlobFixture(t, root, "memory/hidden.go")
		writeGlobFixture(t, root, "node_modules/hidden.go")
		writeGlobFixture(t, root, ".git/hidden.go")

		got, err := NewGlob(config.Defaults(root).Tools.FindFiles).Call(context.Background(), &session.Session{Workspace: root}, map[string]any{"pattern": "*.go"})
		if err != nil {
			t.Fatal(err)
		}
		want := "nested/a.go\nnested/b.go\nz.go"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
		got, err = NewGlob(config.Defaults(root).Tools.FindFiles).Call(context.Background(), &session.Session{Workspace: root}, map[string]any{"pattern": "**/*.go"})
		if err != nil || got != want {
			t.Fatalf("recursive pattern got %q, %v; want %q", got, err, want)
		}
	})

	t.Run("no_match", func(t *testing.T) {
		root := t.TempDir()
		writeGlobFixture(t, root, "only.txt")
		got, err := NewGlob(config.Defaults(root).Tools.FindFiles).Call(context.Background(), &session.Session{Workspace: root}, map[string]any{"pattern": "*.go"})
		if err != nil || got != "no matches" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("relative_parent_uses_OS_reach", func(t *testing.T) {
		root := t.TempDir()
		_, err := NewGlob(config.Defaults(root).Tools.FindFiles).Call(context.Background(), &session.Session{Workspace: root}, map[string]any{"pattern": "*", "path": ".."})
		if err != nil {
			t.Fatalf("error %v", err)
		}
	})

	t.Run("truncation_marker", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < globMaxResults+3; i++ {
			writeGlobFixture(t, root, fmt.Sprintf("file-%03d.txt", i))
		}
		got, err := NewGlob(config.Defaults(root).Tools.FindFiles).Call(context.Background(), &session.Session{Workspace: root}, map[string]any{"pattern": "*.txt"})
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(got, "\n")
		if len(lines) != globMaxResults+1 {
			t.Fatalf("got %d output lines, want %d", len(lines), globMaxResults+1)
		}
		if lines[len(lines)-1] != "[truncated: 200 of 203 paths shown]" {
			t.Fatalf("missing explicit truncation marker: %q", lines[len(lines)-1])
		}
	})
}

func TestConfiguredProtectedRootsMatchVolumeRelativePrefixes(t *testing.T) {
	volumeRoot := string(filepath.Separator)
	if volume := filepath.VolumeName(t.TempDir()); volume != "" {
		volumeRoot = volume + string(filepath.Separator)
	}
	patterns := config.Defaults(t.TempDir()).Tools.FindFiles.SkipRoots
	for _, candidate := range []string{
		filepath.Join(volumeRoot, "Windows", "System32"),
		filepath.Join(volumeRoot, "$Recycle.Bin", "account"),
		filepath.Join(volumeRoot, "System Volume Information", "tracking.log"),
		filepath.Join(volumeRoot, "ProgramData", "Microsoft", "Windows Defender Advanced Threat Protection", "Classification"),
		filepath.Join(volumeRoot, "Program Files", "Windows Defender", "scan.dll"),
	} {
		if !configuredSkipRoot(candidate, patterns) {
			t.Errorf("protected path did not match: %s", candidate)
		}
	}
	if configuredSkipRoot(filepath.Join(volumeRoot, "ProgramData", "ordinary", "file.txt"), patterns) {
		t.Fatal("ordinary root matched protected patterns")
	}
}

func TestNameSearchDeadlineReturnsPartialOrTimeoutAndLeavesFastResultsAlone2xd(t *testing.T) {
	root := t.TempDir()
	writeGlobFixture(t, root, "nested/hit.txt")
	walk := func(includeMatch, block bool) func(context.Context, string, fs.WalkDirFunc) error {
		return func(ctx context.Context, walkRoot string, visit fs.WalkDirFunc) error {
			rootInfo, err := os.Stat(walkRoot)
			if err != nil {
				return err
			}
			if err := visit(walkRoot, fs.FileInfoToDirEntry(rootInfo), nil); err != nil {
				return err
			}
			if includeMatch {
				path := filepath.Join(walkRoot, "nested", "hit.txt")
				info, err := os.Stat(path)
				if err != nil {
					return err
				}
				if err := visit(path, fs.FileInfoToDirEntry(info), nil); err != nil {
					return err
				}
			}
			if block {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}
	}

	for _, test := range []struct {
		name, want           string
		match, block, failed bool
	}{{"partial", "nested/hit.txt", true, true, false}, {"empty", "timed out", false, true, true}, {"fast", "nested/hit.txt", true, false, false}} {
		t.Run(test.name, func(t *testing.T) {
			tool := NewGlobWithDeadline(config.Defaults(root).Tools.FindFiles, 20*time.Millisecond, walk(test.match, test.block))
			started := time.Now()
			detail := tool.CallDetailed(context.Background(), &session.Session{Workspace: root}, map[string]any{"pattern": "*.txt"})
			if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
				t.Fatalf("elapsed=%s", elapsed)
			}
			if test.failed != (detail.Err != nil) || !strings.Contains(detail.Content+fmt.Sprint(detail.Err), test.want) {
				t.Fatalf("detail=%+v", detail)
			}
			truncated, present := detail.Metadata["truncated"].(bool)
			if test.block != (present && truncated) {
				t.Fatalf("metadata=%#v", detail.Metadata)
			}
			if test.block && (!strings.Contains(detail.Content+fmt.Sprint(detail.Err), "stopped after") || !strings.Contains(detail.Content+fmt.Sprint(detail.Err), "last directory")) {
				t.Fatalf("missing truthful deadline sentence: %+v", detail)
			}
		})
	}
	if description := NewSearch(NewGlob(config.Defaults(root).Tools.FindFiles), NewGlob(config.Defaults(root).Tools.FindFiles)).Description(); !strings.Contains(description, "30 seconds") {
		t.Fatalf("description=%q", description)
	}
}

func writeGlobFixture(t *testing.T, root, name string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
}
