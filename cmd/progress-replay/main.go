package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"harness/internal/events"
	"harness/internal/progress"
)

// Item 2lv: this reads a journal as it is STORED.
//
// It used to decode a JSON array from stdin, and a journal is JSONL — so
// pointing it at one meant writing a conversion step, every time, by hand. Item
// 2iy needed exactly that and wrote the shim; rel-1.18.0 carded it.
//
// Three things change and nothing else does: it reads JSONL, it reads a PATH as
// well as stdin because naming a file is how anyone uses this, and a bad line
// is reported with its number and skipped rather than ending the run — a
// journal being diagnosed is often a journal that went wrong, and stopping at
// the first bad line defeats the tool.

// readResult is what one input yielded, with what it could not read.
type readResult struct {
	events    []events.Event
	bad       []string
	wrapped   int
	bare      int
	isArray   bool
	linesRead int
}

// record is the journal's own line shape: the event inside a durable envelope
// carrying its cursor. A journal holds these; a hand-made array holds the events
// themselves.
type record struct {
	Event events.Event `json:"event"`
}

func main() {
	var reader io.Reader = os.Stdin
	source := "standard input"
	// (a): a path as well as stdin.
	if len(os.Args) > 1 {
		arg := os.Args[1]
		if arg == "-h" || arg == "--help" {
			fmt.Println("progress-replay [JOURNAL]\n\nReads a session journal (JSONL, as stored) or a JSON array of\nevents, on standard input or from the named file, and prints the\nstuck-run detectors' records for it.")
			return
		}
		file, err := os.Open(arg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer file.Close()
		reader, source = file, arg
	}

	result, err := read(reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", source, err)
		os.Exit(2)
	}
	if len(result.events) == 0 {
		// (d): empty input is not a crash and not silence. It is a sentence.
		fmt.Fprintf(os.Stderr, "%s held no events (%d line(s) read, %d unreadable)\n", source, result.linesRead, len(result.bad))
		reportBad(result)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(progress.EvaluateEvents(result.events, progress.ArmedSet(false))); err != nil {
		fmt.Fprintln(os.Stderr, "encode detector records:", err)
		os.Exit(2)
	}
	// (d): what could not be parsed is summarized at the END, on stderr, so it
	// is not lost and does not corrupt the records on stdout.
	reportBad(result)
}

// reportBad summarizes the unreadable lines. It is the whole of (d): a tool that
// silently drops what it cannot read tells you the journal is fine.
func reportBad(result readResult) {
	if len(result.bad) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\n%d unreadable line(s), skipped:\n", len(result.bad))
	const shown = 20
	for index, message := range result.bad {
		if index == shown {
			fmt.Fprintf(os.Stderr, "  … and %d more\n", len(result.bad)-shown)
			break
		}
		fmt.Fprintf(os.Stderr, "  %s\n", message)
	}
}

// read decides what it is looking at BY LOOKING, not by a flag: (b). The first
// non-space byte is '[' for an array and '{' for JSONL, and nothing else is
// either.
func read(reader io.Reader) (readResult, error) {
	buffered := bufio.NewReaderSize(reader, 1<<20)
	for {
		first, err := buffered.Peek(1)
		if err != nil {
			if err == io.EOF {
				return readResult{}, nil
			}
			return readResult{}, err
		}
		if first[0] == ' ' || first[0] == '\t' || first[0] == '\r' || first[0] == '\n' {
			if _, err := buffered.Discard(1); err != nil {
				return readResult{}, err
			}
			continue
		}
		if first[0] == '[' {
			return readArray(buffered)
		}
		return readJSONL(buffered), nil
	}
}

// readArray keeps the form something already depends on. rel-1.19.0/W0 checked
// whether anything does before keeping it: tests/progress-replay.mjs spawns this
// with JSON.stringify(events) on stdin, so (b) is a live path rather than a
// compatibility gesture.
func readArray(reader io.Reader) (readResult, error) {
	var stream []events.Event
	if err := json.NewDecoder(reader).Decode(&stream); err != nil {
		return readResult{}, fmt.Errorf("decode event array: %w", err)
	}
	return readResult{events: stream, isArray: true, linesRead: 1}, nil
}

// readJSONL reads a journal line by line. A journal line is a durable record
// wrapping its event; a looser file may hold bare events. Both are accepted
// because both exist on disk, and which one a line is is decided by whether the
// wrapper produced an event with a type.
func readJSONL(reader *bufio.Reader) readResult {
	result := readResult{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	number := 0
	for scanner.Scan() {
		number++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		result.linesRead++
		if !strings.HasPrefix(line, "{") {
			result.bad = append(result.bad, fmt.Sprintf("line %d: not a JSON object", number))
			continue
		}
		var wrapped record
		if err := json.Unmarshal([]byte(line), &wrapped); err == nil && wrapped.Event.Type != "" {
			result.events = append(result.events, wrapped.Event)
			result.wrapped++
			continue
		}
		var bare events.Event
		if err := json.Unmarshal([]byte(line), &bare); err != nil {
			// (c): the line NUMBER, and the run continues.
			result.bad = append(result.bad, fmt.Sprintf("line %d: %v", number, err))
			continue
		}
		if bare.Type == "" {
			result.bad = append(result.bad, fmt.Sprintf("line %d: no event type", number))
			continue
		}
		result.events = append(result.events, bare)
		result.bare++
	}
	if err := scanner.Err(); err != nil {
		result.bad = append(result.bad, fmt.Sprintf("after line %d: %v", number, err))
	}
	return result
}
