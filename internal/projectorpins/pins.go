package projectorpins

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"harness/internal/events"
	"harness/internal/projection"
)

const ManifestSchema = 2

type Manifest struct {
	SchemaVersion int    `json:"schema_version"`
	Cases         []Case `json:"cases"`
}

type Case struct {
	ID                string   `json:"id"`
	Source            string   `json:"source"`
	Golden            string   `json:"golden"`
	Origin            string   `json:"origin"`
	OriginSHA256      string   `json:"origin_sha256"`
	OriginRecords     int      `json:"origin_records"`
	DecompressedBytes int64    `json:"decompressed_bytes"`
	PredecessorCase   string   `json:"predecessor_case,omitempty"`
	Slow              bool     `json:"slow,omitempty"`
	Shapes            []string `json:"shapes"`
	Rationale         string   `json:"rationale"`
}

func RepoRoot(start string) (string, error) {
	value, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(value, "go.mod")); statErr == nil {
			return value, nil
		}
		parent := filepath.Dir(value)
		if parent == value {
			return "", fmt.Errorf("go.mod not found above %s", start)
		}
		value = parent
	}
}

func LoadManifest(root string) (Manifest, string, error) {
	path := filepath.Join(root, "internal", "projection", "testdata", "pins", "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, "", err
	}
	var value Manifest
	if err := json.Unmarshal(data, &value); err != nil {
		return Manifest{}, "", err
	}
	if value.SchemaVersion != ManifestSchema {
		return Manifest{}, "", fmt.Errorf("pin manifest schema %d, want %d", value.SchemaVersion, ManifestSchema)
	}
	return value, filepath.Dir(path), nil
}

func WriteGoldens(root string) error {
	manifest, dir, err := LoadManifest(root)
	if err != nil {
		return err
	}
	materialized, cleanup, err := materializeFixtures(manifest, dir)
	if err != nil {
		return err
	}
	defer cleanup()
	for _, item := range manifest.Cases {
		value, projectErr := projection.GoldenFile(materialized[item.ID])
		if projectErr != nil {
			return fmt.Errorf("%s: %w", item.ID, projectErr)
		}
		data, marshalErr := projection.MarshalGolden(value)
		if marshalErr != nil {
			return fmt.Errorf("%s: %w", item.ID, marshalErr)
		}
		path := filepath.Join(dir, filepath.FromSlash(item.Golden))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		fmt.Printf("updated %s (%d records)\n", filepath.ToSlash(item.Golden), len(value.Records))
	}
	return nil
}

func Verify(root string) error { return verify(root, false) }

func VerifySlow(root string) error { return verify(root, true) }

func verify(root string, slowOnly bool) error {
	manifest, dir, err := LoadManifest(root)
	if err != nil {
		return err
	}
	materialized, cleanup, err := materializeFixtures(manifest, dir)
	if err != nil {
		return err
	}
	defer cleanup()
	for _, item := range manifest.Cases {
		if item.Slow != slowOnly {
			continue
		}
		value, projectErr := projection.GoldenFile(materialized[item.ID])
		if projectErr != nil {
			return fmt.Errorf("%s: %w", item.ID, projectErr)
		}
		actual, marshalErr := projection.MarshalGolden(value)
		if marshalErr != nil {
			return fmt.Errorf("%s: %w", item.ID, marshalErr)
		}
		expected, readErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(item.Golden)))
		if readErr != nil {
			return fmt.Errorf("%s: %w", item.ID, readErr)
		}
		if !bytes.Equal(actual, expected) {
			return describeMismatch(item.ID, expected, actual)
		}
	}
	return nil
}

func materializeFixtures(manifest Manifest, dir string) (map[string]string, func(), error) {
	temporary, err := os.MkdirTemp("", "agentb-projector-pins-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(temporary) }
	result := make(map[string]string, len(manifest.Cases))
	for _, item := range manifest.Cases {
		data, readErr := readFixture(filepath.Join(dir, filepath.FromSlash(item.Source)))
		if readErr != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("%s: %w", item.ID, readErr)
		}
		if lengthErr := validateFixtureLength(item.ID, data, item.DecompressedBytes); lengthErr != nil {
			cleanup()
			return nil, func() {}, lengthErr
		}
		name := strings.TrimSuffix(filepath.Base(item.Source), ".gz")
		path := filepath.Join(temporary, name)
		if writeErr := os.WriteFile(path, data, 0o644); writeErr != nil {
			cleanup()
			return nil, func() {}, writeErr
		}
		result[item.ID] = path
	}
	return result, cleanup, nil
}

func readFixture(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("open gzip fixture: %w", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("decompress fixture: %w", err)
	}
	return data, nil
}

func validateFixtureLength(id string, data []byte, want int64) error {
	if int64(len(data)) != want {
		return fmt.Errorf("fixture %s decompressed to %d bytes, want %d; line endings may have changed", id, len(data), want)
	}
	return nil
}

func describeMismatch(id string, expected, actual []byte) error {
	var left, right projection.GoldenMaster
	if json.Unmarshal(expected, &left) != nil || json.Unmarshal(actual, &right) != nil {
		return fmt.Errorf("golden %s differs (invalid golden JSON)", id)
	}
	limit := min(len(left.Records), len(right.Records))
	for index := 0; index < limit; index++ {
		l, _ := json.Marshal(left.Records[index])
		r, _ := json.Marshal(right.Records[index])
		if !bytes.Equal(l, r) {
			return fmt.Errorf("golden %s differs at record %d (%s), cursor %s:%d", id, index+1,
				right.Records[index].EventType, right.Records[index].Cursor.Generation, right.Records[index].Cursor.Offset)
		}
	}
	if len(left.Records) != len(right.Records) {
		return fmt.Errorf("golden %s record count differs: want %d, got %d", id, len(left.Records), len(right.Records))
	}
	return fmt.Errorf("golden %s final snapshot differs after record %d", id, len(right.Records))
}

func ImportSources(root string) error {
	manifest, dir, err := LoadManifest(root)
	if err != nil {
		return err
	}
	byID := map[string]Case{}
	sources := map[string][]byte{}
	for _, item := range manifest.Cases {
		byID[item.ID] = item
		origin := filepath.Join(root, filepath.FromSlash(item.Origin))
		if err := allowedOrigin(root, origin); err != nil {
			return fmt.Errorf("%s: %w", item.ID, err)
		}
		data, records, importErr := scrubLog(origin)
		if importErr != nil {
			return fmt.Errorf("%s: %w", item.ID, importErr)
		}
		raw, readErr := os.ReadFile(origin)
		if readErr != nil {
			return fmt.Errorf("%s: %w", item.ID, readErr)
		}
		sum := sha256.Sum256(raw)
		if item.OriginSHA256 != "" && item.OriginSHA256 != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("%s: source hash changed: got %s", item.ID, hex.EncodeToString(sum[:]))
		}
		if item.OriginRecords != 0 && item.OriginRecords != records {
			return fmt.Errorf("%s: source records changed: got %d", item.ID, records)
		}
		sources[item.ID] = data
	}
	for _, item := range manifest.Cases {
		if item.PredecessorCase == "" {
			continue
		}
		predecessor, ok := byID[item.PredecessorCase]
		if !ok {
			return fmt.Errorf("%s: predecessor case %q not found", item.ID, item.PredecessorCase)
		}
		generation := strings.TrimSuffix(filepath.Base(predecessor.Source), ".gz")
		updated, err := rewritePredecessor(sources[item.ID], generation, int64(len(sources[predecessor.ID])))
		if err != nil {
			return fmt.Errorf("%s: %w", item.ID, err)
		}
		sources[item.ID] = updated
	}
	for _, item := range manifest.Cases {
		data := sources[item.ID]
		if err := validateFixtureLength(item.ID, data, item.DecompressedBytes); err != nil {
			return err
		}
		path := filepath.Join(dir, filepath.FromSlash(item.Source))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writeGzip(path, data); err != nil {
			return err
		}
	}
	return nil
}

func writeGzip(path string, data []byte) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	writer, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		_ = file.Close()
		return err
	}
	writer.Header.ModTime = time.Unix(0, 0).UTC()
	writer.Header.OS = 255
	_, err = writer.Write(data)
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func allowedOrigin(root, path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	protected := strings.ToLower(filepath.Clean(`C:\alpha`) + string(filepath.Separator))
	if strings.HasPrefix(strings.ToLower(absolute)+string(filepath.Separator), protected) {
		return fmt.Errorf("alpha path is forbidden")
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || strings.HasPrefix(relative, "..") {
		return fmt.Errorf("source is outside repository")
	}
	allowed := []string{"logs" + string(filepath.Separator), ".tools" + string(filepath.Separator) + "step5-data" + string(filepath.Separator) + "logs" + string(filepath.Separator), "serve" + string(filepath.Separator) + "probes" + string(filepath.Separator) + "reliability" + string(filepath.Separator) + "runs" + string(filepath.Separator)}
	value := filepath.Clean(relative) + string(filepath.Separator)
	for _, prefix := range allowed {
		if strings.HasPrefix(strings.ToLower(value), strings.ToLower(prefix)) {
			return nil
		}
	}
	return fmt.Errorf("source is outside approved recorded-log roots")
}

func scrubLog(path string) ([]byte, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	var output bytes.Buffer
	var first time.Time
	records := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var event events.Event
			if err := json.Unmarshal(bytes.TrimSpace(line), &event); err != nil {
				return nil, records, fmt.Errorf("record %d: %w", records+1, err)
			}
			at, parseErr := time.Parse(time.RFC3339Nano, event.TS)
			if parseErr != nil {
				at = time.UnixMilli(int64(records))
			}
			if first.IsZero() {
				first = at
			}
			event.TS = time.Unix(0, 0).UTC().Add(at.Sub(first)).Format(time.RFC3339Nano)
			event.Body, event.Raw = nil, nil
			event.Data = scrubValue("", event.Data)
			encoded, err := json.Marshal(event)
			if err != nil {
				return nil, records, err
			}
			output.Write(encoded)
			output.WriteByte('\n')
			records++
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, records, readErr
		}
	}
	return output.Bytes(), records, nil
}

func scrubValue(key string, value any) any {
	switch current := value.(type) {
	case string:
		if current == "" || safeString(key, current) {
			return current
		}
		sum := sha256.Sum256([]byte(current))
		return fmt.Sprintf("<r:%s:%d>", hex.EncodeToString(sum[:6]), len([]byte(current)))
	case map[string]any:
		result := make(map[string]any, len(current))
		keys := make([]string, 0, len(current))
		for child := range current {
			keys = append(keys, child)
		}
		sort.Strings(keys)
		for _, child := range keys {
			result[child] = scrubValue(child, current[child])
		}
		return result
	case []any:
		result := make([]any, len(current))
		for index := range current {
			result[index] = scrubValue(key, current[index])
		}
		return result
	default:
		return value
	}
}

func safeString(key, value string) bool {
	if key == "id" || key == "name" || key == "generation" || strings.HasSuffix(key, "_id") {
		return true
	}
	if key == "estimated_categories" {
		return true
	}
	safe := map[string]bool{
		"user": true, "assistant": true, "tool": true, "system": true,
		"history": true, "files": true, "fetched": true, "results": true, "summary": true, "memory": true,
		"idle": true, "queued": true, "running": true, "paused": true, "stopping": true,
		"enter": true, "exit": true, "assemble": true, "call_model": true, "parse": true, "dispatch": true, "execute": true, "append": true, "compact": true, "wait_user": true,
		"done": true, "user_stop": true, "turn_ceiling": true, "cycle": true, "tool_errors": true, "context_ceiling": true, "length": true, "model_error": true, "profile_not_runnable": true,
		"reasoning": true, "content": true, "exact": true, "estimated": true, "approve": true, "deny": true, "summarize": true, "elide": true,
	}
	return safe[value]
}

func rewritePredecessor(data []byte, generation string, offset int64) ([]byte, error) {
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) == 0 || len(bytes.TrimSpace(lines[0])) == 0 {
		return nil, fmt.Errorf("empty reset source")
	}
	var event events.Event
	if err := json.Unmarshal(lines[0], &event); err != nil {
		return nil, err
	}
	if event.Type != events.SessionReset {
		return nil, fmt.Errorf("first record is %s, want %s", event.Type, events.SessionReset)
	}
	dataMap, ok := event.Data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("reset data is not an object")
	}
	dataMap["predecessor"] = map[string]any{"generation": generation, "offset": offset}
	first, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	lines[0] = first
	return bytes.Join(lines, []byte{'\n'}), nil
}
