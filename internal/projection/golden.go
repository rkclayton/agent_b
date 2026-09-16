package projection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"harness/internal/events"
)

// Small scalar/object values stay visible in review. Larger payloads retain
// their exact byte count and digest so token streams do not dominate the pin.
const goldenInlineValueLimit = 160

// GoldenMaster is the deterministic projector-output boundary used by the
// checked-in pins. It records every transition so an intermediate regression
// cannot be hidden by a later event that overwrites the affected field.
type GoldenMaster struct {
	SchemaVersion int                `json:"schema_version"`
	Source        string             `json:"source"`
	Records       []GoldenTransition `json:"records"`
	Snapshot      json.RawMessage    `json:"snapshot"`
}

type GoldenTransition struct {
	Index      int               `json:"index"`
	Cursor     Cursor            `json:"cursor"`
	EventType  string            `json:"event_type"`
	Operations []GoldenOperation `json:"operations"`
}

type GoldenOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

type goldenDigest struct {
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// GoldenFile projects a fixture through the same Next transition used live,
// then removes runtime clocks and duration instruments before producing pins.
func GoldenFile(path string) (GoldenMaster, error) {
	records, _, err := ReadFile(path, 0)
	if err != nil {
		return GoldenMaster{}, err
	}
	state := Empty(sessionID(records))
	if predecessor, ok := predecessorCursor(records); ok {
		previousPath := filepath.Join(filepath.Dir(path), predecessor.Generation)
		state, _, err = ProjectFile(previousPath, predecessor.Offset)
		if err != nil {
			return GoldenMaster{}, fmt.Errorf("project golden predecessor %s at byte %d: %w", previousPath, predecessor.Offset, err)
		}
	}
	result := GoldenMaster{SchemaVersion: SchemaVersion, Source: filepath.Base(path), Records: make([]GoldenTransition, 0, len(records))}
	for index, record := range records {
		next, patch, nextErr := Next(state, record)
		if nextErr != nil {
			return GoldenMaster{}, fmt.Errorf("project golden %s at record %d byte %d: %w", path, index+1, record.Cursor.Offset, nextErr)
		}
		operations := make([]GoldenOperation, 0, len(patch.Operations))
		for _, operation := range patch.Operations {
			operations = append(operations, goldenOperation(operation))
		}
		result.Records = append(result.Records, GoldenTransition{
			Index: index + 1, Cursor: record.Cursor, EventType: record.Event.Type, Operations: operations,
		})
		state = next
	}
	result.Snapshot, err = goldenSnapshotJSON(state)
	if err != nil {
		return GoldenMaster{}, err
	}
	return result, nil
}

func MarshalGolden(value GoldenMaster) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString("{\n  \"schema_version\": ")
	output.WriteString(fmt.Sprint(value.SchemaVersion))
	output.WriteString(",\n  \"source\": ")
	source, _ := json.Marshal(value.Source)
	output.Write(source)
	output.WriteString(",\n  \"records\": [\n")
	for index, record := range value.Records {
		data, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		output.WriteString("    ")
		output.Write(data)
		if index+1 < len(value.Records) {
			output.WriteByte(',')
		}
		output.WriteByte('\n')
	}
	output.WriteString("  ],\n  \"snapshot\": ")
	output.Write(value.Snapshot)
	output.WriteString("\n}\n")
	return output.Bytes(), nil
}

func GoldenDigest(path string) (string, int, error) {
	value, err := GoldenFile(path)
	if err != nil {
		return "", 0, err
	}
	data, err := MarshalGolden(value)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), len(value.Records), nil
}

func goldenOperation(value Operation) GoldenOperation {
	result := GoldenOperation{Op: value.Op, Path: value.Path}
	if len(value.Value) == 0 {
		return result
	}
	var decoded any
	if err := json.Unmarshal(value.Value, &decoded); err != nil {
		decoded = string(value.Value)
	}
	compact, _ := json.Marshal(goldenValue(decoded))
	if len(compact) <= goldenInlineValueLimit {
		result.Value = compact
		return result
	}
	sum := sha256.Sum256(compact)
	digest, _ := json.Marshal(goldenDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: len(compact)})
	result.Value = digest
	return result
}

func goldenSnapshot(value Snapshot) Snapshot {
	data, _ := json.Marshal(value)
	var result Snapshot
	_ = json.Unmarshal(data, &result)
	result.Activity.LastTimings = nil
	result.Activity.Progress = goldenMap(result.Activity.Progress)
	if result.Activity.Stream != nil {
		result.Activity.Stream.StartedAt = 0
		result.Activity.Stream.LastChunkAt = 0
		result.Activity.Stream.RateStartedAt = 0
		result.Activity.Stream.Rate = 0
		result.Activity.Stream.Timings = nil
	}
	for index := range result.Timeline {
		result.Timeline[index] = goldenEvent(result.Timeline[index])
	}
	for index := range result.Chat {
		entry := &result.Chat[index]
		entry.ThinkingStartedMS = 0
		entry.ThinkingEndedMS = 0
		entry.ThinkingMS = nil
		entry.Result = goldenMap(entry.Result)
		if entry.Event != nil {
			event := goldenEvent(*entry.Event)
			entry.Event = &event
		}
	}
	return result
}

func goldenSnapshotJSON(value Snapshot) (json.RawMessage, error) {
	data, err := json.Marshal(goldenSnapshot(value))
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return json.Marshal(goldenValue(decoded))
}

func goldenEvent(value events.Event) events.Event {
	value.TS = ""
	value.Body = nil
	value.Raw = nil
	value.Data = goldenValue(value.Data)
	return value
}

func goldenMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result, _ := goldenValue(value).(map[string]any)
	return result
}

func goldenValue(value any) any {
	switch current := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(current))
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if goldenClockKey(key) {
				continue
			}
			result[key] = goldenValue(current[key])
		}
		return result
	case []any:
		result := make([]any, len(current))
		for index := range current {
			result[index] = goldenValue(current[index])
		}
		return result
	default:
		return value
	}
}

func goldenClockKey(key string) bool {
	value := strings.ToLower(key)
	return value == "ts" || value == "timings" || value == "duration" || value == "elapsed" ||
		value == "time" || value == "rate" || strings.HasSuffix(value, "_ms") || strings.HasSuffix(value, "_at") ||
		strings.HasSuffix(value, "_per_second")
}
