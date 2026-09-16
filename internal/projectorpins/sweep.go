package projectorpins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"harness/internal/projection"
)

type SweepBaseline struct {
	SchemaVersion   int          `json:"schema_version"`
	ProjectorSchema int          `json:"projector_schema"`
	Logs            []SweepEntry `json:"logs"`
}

type SweepEntry struct {
	Path             string `json:"path"`
	SourceSHA256     string `json:"source_sha256"`
	Records          int    `json:"records"`
	ProjectionSHA256 string `json:"projection_sha256"`
}

type SweepResult struct {
	Compared, Same, Different, New, Missing, Errors, Skipped int
}

func Sweep(root string, record bool) (SweepResult, error) {
	path := filepath.Join(root, "internal", "projection", "testdata", "pins", "sweep-baseline.json")
	baseline := SweepBaseline{SchemaVersion: 1, ProjectorSchema: projection.SchemaVersion, Logs: []SweepEntry{}}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &baseline); err != nil {
			return SweepResult{}, err
		}
	} else if !record {
		return SweepResult{}, fmt.Errorf("read sweep baseline: %w", err)
	}
	current, result := collectSweep(root)
	if record {
		baseline = SweepBaseline{SchemaVersion: 1, ProjectorSchema: projection.SchemaVersion, Logs: current}
		data, err := json.MarshalIndent(baseline, "", "  ")
		if err != nil {
			return result, err
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			return result, err
		}
		fmt.Printf("recorded wide baseline: %d session logs, %d errors, %d skipped\n", len(current), result.Errors, result.Skipped)
		return result, nil
	}
	want := map[string]SweepEntry{}
	for _, entry := range baseline.Logs {
		want[entry.Path] = entry
	}
	seen := map[string]bool{}
	for _, entry := range current {
		seen[entry.Path] = true
		previous, ok := want[entry.Path]
		if !ok {
			result.New++
			fmt.Printf("NEW %s (%d records)\n", entry.Path, entry.Records)
			continue
		}
		result.Compared++
		if previous.SourceSHA256 == entry.SourceSHA256 && previous.ProjectionSHA256 == entry.ProjectionSHA256 && previous.Records == entry.Records {
			result.Same++
			continue
		}
		result.Different++
		fmt.Printf("DIFF %s source=%t records=%d->%d projection=%s->%s\n", entry.Path,
			previous.SourceSHA256 != entry.SourceSHA256, previous.Records, entry.Records,
			shortHash(previous.ProjectionSHA256), shortHash(entry.ProjectionSHA256))
	}
	for _, entry := range baseline.Logs {
		if !seen[entry.Path] {
			result.Missing++
			fmt.Printf("MISSING %s\n", entry.Path)
		}
	}
	fmt.Printf("wide sweep: compared=%d same=%d different=%d new=%d missing=%d errors=%d skipped=%d (non-blocking)\n",
		result.Compared, result.Same, result.Different, result.New, result.Missing, result.Errors, result.Skipped)
	return result, nil
}

func collectSweep(root string) ([]SweepEntry, SweepResult) {
	roots := []string{
		filepath.Join(root, "logs"),
		filepath.Join(root, ".tools", "step5-data", "logs"),
		filepath.Join(root, "serve", "probes", "reliability", "runs"),
	}
	var paths []string
	for _, base := range roots {
		_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err == nil && info != nil && !info.IsDir() && strings.EqualFold(filepath.Ext(path), ".jsonl") {
				paths = append(paths, path)
			}
			return nil
		})
	}
	sort.Strings(paths)
	result := SweepResult{}
	entries := make([]SweepEntry, 0, len(paths))
	for _, path := range paths {
		records, _, err := projection.ReadFile(path, 0)
		if err != nil {
			result.Errors++
			fmt.Printf("ERROR %s: %v\n", relativeSlash(root, path), err)
			continue
		}
		session := false
		for _, record := range records {
			if record.Event.SessionID != "" {
				session = true
				break
			}
		}
		if !session {
			result.Skipped++
			continue
		}
		projectionHash, count, err := projection.GoldenDigest(path)
		if err != nil {
			result.Errors++
			fmt.Printf("ERROR %s: %v\n", relativeSlash(root, path), err)
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			result.Errors++
			continue
		}
		sum := sha256.Sum256(data)
		entries = append(entries, SweepEntry{Path: relativeSlash(root, path), SourceSHA256: hex.EncodeToString(sum[:]), Records: count, ProjectionSHA256: projectionHash})
	}
	return entries, result
}

func relativeSlash(root, path string) string {
	value, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(value)
}

func shortHash(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
