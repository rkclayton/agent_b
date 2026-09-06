package projectorpins

import (
	"os"
	"path/filepath"
	"testing"

	"harness/internal/projection"
)

var profileSnapshot projection.Snapshot

func BenchmarkProjectionRecursive(b *testing.B) {
	root, err := RepoRoot(".")
	if err != nil {
		b.Fatal(err)
	}
	manifest, dir, err := LoadManifest(root)
	if err != nil {
		b.Fatal(err)
	}
	paths, cleanup, err := materializeFixtures(manifest, dir)
	if err != nil {
		b.Fatal(err)
	}
	defer cleanup()
	path := paths["recursive-compaction"]
	records, _, err := projection.ReadFile(path, 0)
	if err != nil {
		b.Fatal(err)
	}
	sessionID := ""
	for _, record := range records {
		if record.Event.SessionID != "" {
			sessionID = record.Event.SessionID
			break
		}
	}
	b.ReportMetric(float64(len(records)), "records/run")
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		state := projection.Empty(sessionID)
		for _, record := range records {
			state, _, err = projection.Next(state, record)
			if err != nil {
				b.Fatal(err)
			}
		}
		profileSnapshot = state
	}
}

func BenchmarkGoldenGenerationRecursive(b *testing.B) {
	root, err := RepoRoot(".")
	if err != nil {
		b.Fatal(err)
	}
	manifest, dir, err := LoadManifest(root)
	if err != nil {
		b.Fatal(err)
	}
	paths, cleanup, err := materializeFixtures(manifest, dir)
	if err != nil {
		b.Fatal(err)
	}
	defer cleanup()
	path := paths["recursive-compaction"]
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		value, err := projection.GoldenFile(path)
		if err != nil {
			b.Fatal(err)
		}
		if len(value.Records) == 0 {
			b.Fatal("empty golden")
		}
	}
}

func BenchmarkGoldenComparisonRecursive(b *testing.B) {
	root, err := RepoRoot(".")
	if err != nil {
		b.Fatal(err)
	}
	manifest, dir, err := LoadManifest(root)
	if err != nil {
		b.Fatal(err)
	}
	paths, cleanup, err := materializeFixtures(manifest, dir)
	if err != nil {
		b.Fatal(err)
	}
	defer cleanup()
	item := caseByID(manifest, "recursive-compaction")
	expected, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(item.Golden)))
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		value, err := projection.GoldenFile(paths[item.ID])
		if err != nil {
			b.Fatal(err)
		}
		actual, err := projection.MarshalGolden(value)
		if err != nil {
			b.Fatal(err)
		}
		if len(actual) != len(expected) {
			b.Fatalf("golden bytes %d, want %d", len(actual), len(expected))
		}
	}
}

func caseByID(manifest Manifest, id string) Case {
	for _, item := range manifest.Cases {
		if item.ID == id {
			return item
		}
	}
	return Case{}
}
