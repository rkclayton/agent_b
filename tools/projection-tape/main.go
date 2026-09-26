// Item 2lo: the projector can emit a tape, so another client can be tested
// against the real wire.
//
// The projector's goldens are FINAL snapshots. Patches are produced in flight by
// Next and nothing records the sequence, so no artifact describes a session as it
// ARRIVES. The phone client's reducer is therefore tested only against tapes
// hand-built to a written spec -- and the one thing a real tape already
// disproved was a client defect, not a spec disagreement.
//
// This folds a committed `.events` source through Empty then Next and writes one
// plain-JSON artifact per source: the starting snapshot, then every non-empty
// patch in order with the cursors it carried. It reads committed test material
// only: no model, no production, no operator data.
package main

import (
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"harness/internal/projection"
)

type tape struct {
	Source  string               `json:"source"`
	Session string               `json:"session"`
	Start   projection.Snapshot  `json:"start"`
	Patches []projection.Patch   `json:"patches"`
	Counts  tapeCounts           `json:"counts"`
}

// (a): the patch already carries previous_cursor and cursor, which is what the
// wire carries, so the tape records the patch as it would have been sent rather
// than wrapping it in something a consumer would have to unwrap.

type tapeCounts struct {
	Records int `json:"records"`
	Patches int `json:"patches"`
	// (b): an event that changes nothing is not a wire message. Omitted, and
	// counted, so the number is visible rather than silently absorbed.
	EmptyPatches int `json:"empty_patches"`
}

func main() {
	sources := flag.String("sources", filepath.Join("internal", "projection", "testdata", "pins", "sources"), "directory of committed .events sources")
	out := flag.String("out", filepath.Join("internal", "projection", "testdata", "tapes"), "directory to write tapes into")
	measure := flag.Bool("measure", false, "report sizes and write nothing")
	// rel-1.17.0/W0 measured the full set at 47,896,329 bytes. recursive-compaction
	// alone is 36.9 MB from 12,699 records -- 5.2x its own golden -- and
	// known-good-live is 7.2 MB at 4.4x. 2lo's @consequence-if-false applies, and
	// this is the ceiling that applies it: 1,000 records keeps every shape,
	// including the only source that carries a failed tool result (tool-failure,
	// 814 records), and excludes exactly the two that dwarf everything.
	maxRecords := flag.Int("max-records", 1000, "skip a source with more records than this; 0 for no ceiling")
	only := flag.String("only", "", "comma-separated source names, or empty for every source")
	flag.Parse()

	// (c): the source list comes from what exists, so a new pin gets a tape
	// without editing this tool.
	names, err := sourceNames(*sources)
	if err != nil {
		fail(err)
	}
	if strings.TrimSpace(*only) != "" {
		wanted := map[string]bool{}
		for _, name := range strings.Split(*only, ",") {
			wanted[strings.TrimSpace(name)] = true
		}
		kept := names[:0]
		for _, name := range names {
			if wanted[name] {
				kept = append(kept, name)
			}
		}
		names = kept
	}
	if len(names) == 0 {
		fail(fmt.Errorf("no sources found in %s", *sources))
	}

	if !*measure {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			fail(err)
		}
	}
	var totalBytes int
	skipped := []string{}
	for _, name := range names {
		built, err := build(*sources, name)
		if err != nil {
			fail(fmt.Errorf("%s: %w", name, err))
		}
		if *maxRecords > 0 && built.Counts.Records > *maxRecords {
			skipped = append(skipped, fmt.Sprintf("%s (%d records)", name, built.Counts.Records))
			fmt.Printf("%-34s records %6d  SKIPPED: over the %d-record ceiling\n", name, built.Counts.Records, *maxRecords)
			continue
		}
		// (e): plain JSON, indented, so a reader on another machine needs nothing
		// from here to read it.
		encoded, err := json.MarshalIndent(built, "", " ")
		if err != nil {
			fail(err)
		}
		encoded = append(encoded, '\n')
		totalBytes += len(encoded)
		fmt.Printf("%-34s records %6d  patches %6d  empty %5d  bytes %9d\n",
			name, built.Counts.Records, built.Counts.Patches, built.Counts.EmptyPatches, len(encoded))
		if *measure {
			continue
		}
		if err := os.WriteFile(filepath.Join(*out, name+".tape.json"), encoded, 0o644); err != nil {
			fail(err)
		}
	}
	fmt.Printf("%-34s %d tape(s), %d bytes total\n", "TOTAL", len(names)-len(skipped), totalBytes)
	if len(skipped) > 0 {
		// Named, never silent: a consumer reading the tapes directory should be
		// able to see what is not in it.
		fmt.Printf("%-34s %s\n", "SKIPPED", strings.Join(skipped, ", "))
	}
	if !*measure {
		manifest := map[string]any{
			"emitted":     len(names) - len(skipped),
			"skipped":     skipped,
			"max_records": *maxRecords,
			"note":        "Emitted by tools/projection-tape from committed projector pins. Each tape is a starting snapshot and the patches that followed, in order, exactly as the wire carried them.",
		}
		encoded, err := json.MarshalIndent(manifest, "", " ")
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(filepath.Join(*out, "README.json"), append(encoded, '\n'), 0o644); err != nil {
			fail(err)
		}
	}
}

func sourceNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".events.gz"):
			name = strings.TrimSuffix(name, ".events.gz")
		case strings.HasSuffix(name, ".events"):
			name = strings.TrimSuffix(name, ".events")
		default:
			continue
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func build(dir, name string) (tape, error) {
	path := filepath.Join(dir, name+".events")
	if _, err := os.Stat(path); err != nil {
		// The larger pins are committed gzipped. projection.ReadFile takes a path
		// and its reader-based entry is unexported, and 2lo @keep says this item
		// reads the projector and changes nothing -- so the emitter decompresses
		// into a temporary file of its own rather than widening that package.
		gzPath := filepath.Join(dir, name+".events.gz")
		plain, err := decompress(gzPath)
		if err != nil {
			return tape{}, err
		}
		defer os.RemoveAll(filepath.Dir(plain))
		path = plain
	}
	records, _, err := projection.ReadFile(path, 0)
	if err != nil {
		return tape{}, err
	}
	session := ""
	for _, record := range records {
		if id := record.Event.SessionID; id != "" {
			session = id
			break
		}
	}
	snapshot := projection.Empty(session)
	built := tape{Source: name, Session: session, Start: snapshot, Patches: []projection.Patch{}}
	built.Counts.Records = len(records)
	for _, record := range records {
		next, patch, err := projection.Next(snapshot, record)
		if err != nil {
			return tape{}, err
		}
		snapshot = next
		if len(patch.Operations) == 0 {
			built.Counts.EmptyPatches++
			continue
		}
		built.Patches = append(built.Patches, patch)
	}
	built.Counts.Patches = len(built.Patches)
	return built, nil
}

func decompress(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer reader.Close()
	// projection.ReadFile takes the record GENERATION from the file name, so a
	// temporary named after this tool would leak that name into every patch --
	// which the tape gate caught. The temporary keeps the SOURCE name and lives in
	// a directory of its own.
	dir, err := os.MkdirTemp("", "projection-tape")
	if err != nil {
		return "", err
	}
	name := filepath.Join(dir, strings.TrimSuffix(filepath.Base(path), ".gz"))
	temp, err := os.Create(name)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	defer temp.Close()
	if _, err := io.Copy(temp, reader); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return name, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "projection-tape:", err)
	os.Exit(1)
}
