package projection

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"harness/internal/events"
	"harness/internal/tools"
)

const maxRecordBytes = 64 * 1024 * 1024

func ReadFile(path string, through int64) ([]Record, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	var source io.Reader = file
	if through > 0 {
		source = io.LimitReader(file, through)
	}
	return read(filepath.Base(path), source, through)
}

func SlimJournal(path, pending string) (bool, error) {
	return slimJournal(path, pending, nil, tools.AtomicReplace)
}

func slimJournal(path, pending string, alter func([]Record), replace func(string, string) error) (bool, error) {
	records, _, err := ReadFile(path, 0)
	if err != nil {
		return false, err
	}
	merged := false
	if pending != "" {
		if recovered, _, readErr := ReadFile(pending, 0); readErr == nil {
			records = append(records, recovered...)
			merged = len(recovered) > 0
		} else if !os.IsNotExist(readErr) {
			return false, readErr
		}
	}
	full, err := projectRecords(records)
	if err != nil {
		return false, err
	}
	complete := map[string]bool{}
	prepared, reducer := make([]events.Event, len(records)), NewRetainedReducer()
	for index, record := range records {
		prepared[index] = reducer.Prepare(record.Event)
		data := eventMap(record.Event.Data)
		if record.Event.Type == events.ModelResponse {
			complete[turnKey(record.Event, data)] = true
		}
		if record.Event.Type == events.RunStopped {
			complete[record.Event.RunID] = true
		}
	}
	slim := make([]Record, 0, len(records))
	changed, encoded := merged, []byte(nil)
	for index, record := range records {
		if record.Event.Type == events.ModelDelta && (complete[turnKey(record.Event, eventMap(record.Event.Data))] || complete[record.Event.RunID]) {
			changed = true
			continue
		}
		changed = changed || record.Event.Body != nil || record.Event.Raw != nil
		record.Event = prepared[index]
		if record.Event.Type == "" {
			record.Event = stripDiagnostic(records[index].Event)
		}
		slim = append(slim, record)
		line, marshalErr := json.Marshal(record.Event)
		if marshalErr != nil {
			return false, marshalErr
		}
		encoded = append(encoded, line...)
		encoded = append(encoded, '\n')
	}
	if alter != nil {
		alter(slim)
	}
	if !changed && alter == nil {
		return false, nil
	}
	reduced, err := projectRecords(slim)
	if err != nil {
		return false, err
	}
	full.Cursor, reduced.Cursor = Cursor{}, Cursor{}
	if !reflect.DeepEqual(full, reduced) {
		return false, fmt.Errorf("slim projection differs")
	}
	temporary := path + ".slim.tmp"
	if err = os.WriteFile(temporary, encoded, 0o600); err == nil {
		err = replace(temporary, path)
	}
	if err != nil {
		_ = os.Remove(temporary)
		return false, err
	}
	if pending != "" {
		_ = os.Remove(pending)
	}
	return true, nil
}

func projectRecords(records []Record) (state Snapshot, err error) {
	state = Empty(sessionID(records))
	for _, record := range records {
		state, err = NextState(state, record)
		if err != nil {
			return Snapshot{}, err
		}
	}
	return
}

// ProjectFileState is ProjectFile without the patches (item 2gm). Computing
// a patch per record is quadratic in the length of the journal, and a caller
// that wants only the final state should not pay for it.
func ProjectFileState(path string, through int64) (Snapshot, error) {
	state, _, err := projectFile(path, through, false)
	return state, err
}

func ProjectFile(path string, through int64) (Snapshot, []Patch, error) {
	return projectFile(path, through, true)
}

func projectFile(path string, through int64, wantPatches bool) (Snapshot, []Patch, error) {
	records, offset, err := ReadFile(path, through)
	if err != nil {
		return Snapshot{}, nil, err
	}
	state := Empty(sessionID(records))
	if predecessor, ok := predecessorCursor(records); ok {
		previousPath := filepath.Join(filepath.Dir(path), predecessor.Generation)
		previous, previousErr := ProjectFileState(previousPath, predecessor.Offset)
		if previousErr != nil {
			return Snapshot{}, nil, fmt.Errorf("project predecessor %s at byte %d: %w", previousPath, predecessor.Offset, previousErr)
		}
		state = previous
	}
	var patches []Patch
	if wantPatches {
		patches = make([]Patch, 0, len(records))
	}
	for _, record := range records {
		var patch Patch
		if wantPatches {
			state, patch, err = Next(state, record)
		} else {
			state, err = NextState(state, record)
		}
		if err != nil {
			return Snapshot{}, nil, fmt.Errorf("project %s at byte %d: %w", path, record.Cursor.Offset, err)
		}
		if wantPatches {
			patches = append(patches, patch)
		}
	}
	if len(records) == 0 {
		state.Cursor = Cursor{Generation: filepath.Base(path), Offset: offset}
	}
	return state, patches, nil
}

func predecessorCursor(records []Record) (Cursor, bool) {
	if len(records) == 0 || records[0].Event.Type != events.SessionReset {
		return Cursor{}, false
	}
	data := eventMap(records[0].Event.Data)
	value := eventMap(data["predecessor"])
	cursor := Cursor{Generation: stringValue(value["generation"]), Offset: int64(intValue(value["offset"]))}
	return cursor, cursor.Generation != "" && cursor.Offset > 0
}

func read(generation string, source io.Reader, through int64) ([]Record, int64, error) {
	reader := bufio.NewReaderSize(source, 64*1024)
	result := []Record{}
	var offset int64
	line := 0
	for {
		chunk, err := reader.ReadBytes('\n')
		if len(chunk) > 0 {
			if err == io.EOF && through > 0 && chunk[len(chunk)-1] != '\n' {
				return nil, offset, fmt.Errorf("byte offset %d is not a JSONL record boundary", through)
			}
			line++
			if len(chunk) > maxRecordBytes {
				return nil, offset, fmt.Errorf("line %d exceeds %d bytes", line, maxRecordBytes)
			}
			next := offset + int64(len(chunk))
			if through > 0 && next > through {
				return nil, offset, fmt.Errorf("byte offset %d is not a JSONL record boundary", through)
			}
			trimmed := bytes.TrimSpace(chunk)
			if len(trimmed) > 0 {
				var event structEvent
				if decodeErr := json.Unmarshal(trimmed, &event.Event); decodeErr != nil {
					return nil, offset, fmt.Errorf("line %d: %w", line, decodeErr)
				}
				result = append(result, Record{Cursor: Cursor{Generation: generation, Offset: next}, Event: event.Event})
			}
			offset = next
			if through > 0 && offset == through {
				return result, offset, nil
			}
		}
		if err == io.EOF {
			if through > 0 && offset != through {
				return nil, offset, fmt.Errorf("byte offset %d exceeds durable log end %d", through, offset)
			}
			return result, offset, nil
		}
		if err != nil {
			return nil, offset, err
		}
	}
}

type structEvent struct{ Event events.Event }

func sessionID(records []Record) string {
	for _, record := range records {
		if record.Event.SessionID != "" {
			return record.Event.SessionID
		}
	}
	return ""
}
