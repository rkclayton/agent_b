package projectorpins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectorGoldenMasters(t *testing.T) {
	root, err := RepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(root); err != nil {
		t.Fatal(err)
	}
}

func TestManifestPinsTenDistinctRealLogs(t *testing.T) {
	root, err := RepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	manifest, dir, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) < 10 || len(manifest.Cases) > 15 {
		t.Fatalf("curated cases = %d, want 10..15", len(manifest.Cases))
	}
	origins := map[string]bool{}
	shapes := map[string]bool{}
	for _, item := range manifest.Cases {
		if item.Rationale == "" || item.OriginSHA256 == "" || item.OriginRecords == 0 {
			t.Fatalf("case %q lacks provenance or rationale", item.ID)
		}
		if origins[item.Origin] {
			t.Fatalf("raw log %q selected more than once", item.Origin)
		}
		origins[item.Origin] = true
		for _, shape := range item.Shapes {
			shapes[shape] = true
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(item.Source))); err != nil {
			t.Fatalf("case %q source: %v", item.ID, err)
		}
	}
	for _, required := range []string{"predecessor-lineage", "historical-incomplete", "compaction-heavy", "glob-grep-fetch", "write_todos", "read_history", "approval", "long-stream", "tool-failure", "live-deep-equality"} {
		if !shapes[required] {
			t.Errorf("required shape %q is not pinned", required)
		}
	}
}
