package projection

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"harness/internal/events"
)

const maxRecordBytes = 64 * 1024 * 1024

func ReadFile(path string, through int64) ([]Record, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	return read(filepath.Base(path), file, through)
}

func ProjectFile(path string, through int64) (Snapshot, []Patch, error) {
	records, offset, err := ReadFile(path, through)
	if err != nil {
		return Snapshot{}, nil, err
	}
	state := Empty(sessionID(records))
	patches := make([]Patch, 0, len(records))
	for _, record := range records {
		next, patch, nextErr := Next(state, record)
		state, err = next, nextErr
		if err != nil {
			return Snapshot{}, nil, fmt.Errorf("project %s at byte %d: %w", path, record.Cursor.Offset, err)
		}
		patches = append(patches, patch)
	}
	if len(records) == 0 {
		state.Cursor = Cursor{Generation: filepath.Base(path), Offset: offset}
	}
	return state, patches, nil
}

func read(generation string, source io.Reader, through int64) ([]Record, int64, error) {
	reader := bufio.NewReaderSize(source, 64*1024)
	result := []Record{}
	var offset int64
	line := 0
	for {
		chunk, err := reader.ReadBytes('\n')
		if len(chunk) > 0 {
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
