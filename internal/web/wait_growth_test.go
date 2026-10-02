package web

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	attachmentfile "harness/internal/attachment"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/projection"
)

var waitMeasurementSink atomic.Uint64

func TestProjectorSnapshotDoesNotWaitForActiveWriter2pr(t *testing.T) {
	store, writers, bus := projectionStore(t, 3000)
	read := func() { mustSnapshot(t, store, writers) }
	writer := func(stop <-chan struct{}, ready chan<- struct{}) {
		close(ready)
		for index := 0; ; index++ {
			select {
			case <-stop:
				return
			default:
				bus.Publish(events.New(events.MessageQueued, "main", "writer", map[string]any{"position": index + 1}))
			}
		}
	}
	alone := medianWait(read, nil)
	active := medianWait(read, writer)
	t.Logf("2pr snapshot writer wait alone=%s active=%s ratio=%.2fx", alone, active, float64(active)/float64(max(alone, time.Nanosecond)))
	if active > 2*alone {
		t.Fatalf("snapshot waited for writer: alone=%s active=%s ratio=%.2fx", alone, active, float64(active)/float64(alone))
	}
}

// Item 2pq: these are request waits, not throughput benchmarks. Each cell is the
// median of ten waits against an already-built store. The writer arm repeats the
// large read while one ordinary writer for that store is active.
func TestSevenMappedWaitsAtTwoStoredSizes2pq(t *testing.T) {
	type row struct {
		name         string
		small, large func()
		writer       func(<-chan struct{}, chan<- struct{})
	}
	rows := make([]row, 0, 7)

	smallSessions := projectionFixture(3, 300)
	largeSessions := projectionFixture(30, 3000)
	if len(smallSessions["chat-0"].Timeline) != 300 || len(largeSessions["chat-0"].Timeline) != 3000 {
		t.Fatal("timeline fixtures do not carry their stated bounds")
	}
	smallTimeline := timelineFixture(300)
	largeTimeline := timelineFixture(3000)
	readView := func(value projection.Snapshot) func() {
		return func() { waitMeasurementSink.Add(uint64(len(projection.SnapshotForRead(value).Timeline))) }
	}
	rows = append(rows, row{"timeline copy", readView(smallTimeline), readView(largeTimeline), busyCPU})

	smallStore, smallWriters, smallBus := projectionStore(t, 300)
	largeStore, largeWriters, largeBus := projectionStore(t, 3000)
	rows = append(rows, row{"projector snapshot", func() { mustSnapshot(t, smallStore, smallWriters) }, func() { mustSnapshot(t, largeStore, largeWriters) }, func(stop <-chan struct{}, ready chan<- struct{}) {
		close(ready)
		for index := 0; ; index++ {
			select {
			case <-stop:
				return
			default:
				largeBus.Publish(events.New(events.MessageQueued, "main", "writer", map[string]any{"position": index + 1}))
			}
		}
	}})
	_ = smallBus

	configRoot := t.TempDir()
	smallConfig := sizedConfig(t.TempDir(), 3)
	largeConfig := sizedConfig(t.TempDir(), 30)
	var saveSerial atomic.Uint64
	save := func(cfg config.Config, prefix string) func() {
		return func() {
			path := filepath.Join(configRoot, prefix+strconv.FormatUint(saveSerial.Add(1), 10)+".json")
			if err := cfg.Save(path); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows = append(rows, row{"configuration mutation", save(smallConfig, "small-"), save(largeConfig, "large-"), func(stop <-chan struct{}, ready chan<- struct{}) {
		close(ready)
		for index := 0; ; index++ {
			select {
			case <-stop:
				return
			default:
				_ = os.WriteFile(filepath.Join(configRoot, fmt.Sprintf("writer-%d.json", index%2)), []byte("{}\n"), 0o600)
			}
		}
	}})

	stateRoot := t.TempDir()
	stateCfg := sizedConfig(stateRoot, 30)
	stateServer := New(&stateCfg, filepath.Join(stateRoot, "harness.json"), t.TempDir(), RuntimeRoots{Application: t.TempDir(), Data: t.TempDir(), Profile: t.TempDir(), Workspace: stateRoot}, events.NewBus())
	stateRead := func(sessions map[string]projection.Snapshot) func() {
		return func() { mustJSON(t, stateServer.snapshotWithSessions(sessions, false)) }
	}
	smallStateBytes := jsonSize(t, stateServer.snapshotWithSessions(smallSessions, false))
	largeStateBytes := jsonSize(t, stateServer.snapshotWithSessions(largeSessions, false))
	t.Logf("2pr state response scope=whole-timeline small_bytes=%d large_bytes=%d", smallStateBytes, largeStateBytes)
	rows = append(rows, row{"state/config read", stateRead(smallSessions), stateRead(largeSessions), busyCPU})

	attachmentRoot := t.TempDir()
	attachmentDir := filepath.Join(attachmentRoot, "attachments")
	if err := os.MkdirAll(attachmentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	seedAttachments(t, attachmentDir, "small", 30)
	seedAttachments(t, attachmentDir, "large", 300)
	connection := largeConfig.Connections[0]
	var attachmentSerial atomic.Uint64
	store := func(name string) func() {
		return func() {
			_, _, _, err := storeAttachment(attachmentRoot, name+".txt", []byte("payload-"+strconv.FormatUint(attachmentSerial.Add(1), 10)), &connection, attachmentfile.Text)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	rows = append(rows, row{"attachment upload/extraction", store("small"), store("large"), func(stop <-chan struct{}, ready chan<- struct{}) {
		close(ready)
		for index := 0; ; index++ {
			select {
			case <-stop:
				return
			default:
				_ = os.WriteFile(filepath.Join(attachmentDir, fmt.Sprintf("writer-%d.tmp", index%2)), []byte("x"), 0o600)
			}
		}
	}})

	diagnosticsRoot := t.TempDir()
	smallLog := filepath.Join(diagnosticsRoot, "small.log")
	largeLog := filepath.Join(diagnosticsRoot, "large.log")
	writeLines(t, smallLog, 300)
	writeLines(t, largeLog, 3000)
	redactor := newRedactor(diagnosticsRoot, stateServer.roots.Application)
	rows = append(rows, row{"diagnostics export", func() { _ = redactor.tail(smallLog, diagnosticsLogTail) }, func() { _ = redactor.tail(largeLog, diagnosticsLogTail) }, func(stop <-chan struct{}, ready chan<- struct{}) {
		file, err := os.OpenFile(largeLog, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Error(err)
			close(ready)
			return
		}
		defer file.Close()
		close(ready)
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = file.WriteString("writer diagnostic line\n")
			}
		}
	}})

	rows = append(rows, row{"page startup to state", stateRead(smallSessions), stateRead(largeSessions), busyCPU})

	for _, item := range rows {
		small := medianWait(item.small, nil)
		large := medianWait(item.large, nil)
		writer := medianWait(item.large, item.writer)
		ratio := float64(large) / float64(max(small, time.Nanosecond))
		t.Logf("2pq wait %-28s small=%s large=%s ratio=%.2fx writer=%s", item.name, small, large, ratio, writer)
	}
}

func medianWait(operation func(), writer func(<-chan struct{}, chan<- struct{})) time.Duration {
	samples := make([]time.Duration, 10)
	for index := range samples {
		var stop chan struct{}
		var finished chan struct{}
		if writer != nil {
			stop = make(chan struct{})
			ready := make(chan struct{})
			finished = make(chan struct{})
			go func() { writer(stop, ready); close(finished) }()
			<-ready
		}
		started := time.Now()
		for repeat := 0; repeat < 3; repeat++ {
			operation()
		}
		samples[index] = time.Since(started) / 3
		if stop != nil {
			close(stop)
			<-finished
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return (samples[4] + samples[5]) / 2
}

func busyCPU(stop <-chan struct{}, ready chan<- struct{}) {
	close(ready)
	for value := uint64(1); ; value++ {
		select {
		case <-stop:
			return
		default:
			_ = value * 33
		}
	}
}

func projectionFixture(chats, eventsPerChat int) map[string]projection.Snapshot {
	result := make(map[string]projection.Snapshot, chats)
	for chat := 0; chat < chats; chat++ {
		timeline := make([]events.Event, eventsPerChat)
		for index := range timeline {
			timeline[index] = events.New(events.ToolResult, fmt.Sprintf("chat-%d", chat), "run", map[string]any{"call_id": index, "result": "fixture"})
		}
		id := fmt.Sprintf("chat-%d", chat)
		result[id] = projection.Snapshot{SchemaVersion: projection.SchemaVersion, Complete: true, ID: id, Label: id, Timeline: timeline}
	}
	return result
}

func timelineFixture(count int) projection.Snapshot {
	timeline := make([]events.Event, count)
	text := strings.Repeat("x", 512)
	for index := range timeline {
		timeline[index] = events.New(events.ToolResult, "main", "run", map[string]any{"call_id": index, "result": text})
	}
	return projection.Snapshot{SchemaVersion: projection.SchemaVersion, Complete: true, ID: "main", Timeline: timeline}
}

func projectionStore(t *testing.T, count int) (*projection.Store, *events.Writers, *events.Bus) {
	t.Helper()
	writers, err := events.NewWriters(filepath.Join(t.TempDir(), "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	if _, err := writers.OpenSession("main"); err != nil {
		t.Fatal(err)
	}
	store := projection.NewStore()
	bus := events.NewBus()
	bus.SetDurableSink(writers.WriteRecord, store.Apply, store.MarkStale)
	bus.Publish(events.New(events.SessionCreated, "main", "", map[string]any{"session": map[string]any{"id": "main", "label": "main", "run": map[string]any{"status": "idle"}, "tools": []any{}, "messages": []any{}}}))
	for index := 0; index < count; index++ {
		bus.Publish(events.New(events.MessageQueued, "main", "run", map[string]any{"position": index + 1}))
	}
	return store, writers, bus
}

func mustSnapshot(t *testing.T, store *projection.Store, _ *events.Writers) {
	t.Helper()
	_ = store.CurrentSnapshot()
}
func mustJSON(t *testing.T, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	waitMeasurementSink.Add(uint64(len(data)))
}

func jsonSize(t *testing.T, value any) int {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return len(data)
}

func sizedConfig(workspace string, connections int) config.Config {
	cfg := config.Defaults(workspace)
	base := cfg.Connections[0]
	cfg.Connections = make([]config.Connection, connections)
	for index := range cfg.Connections {
		cfg.Connections[index] = base
		if index > 0 {
			cfg.Connections[index].ID = fmt.Sprintf("fixture-%d", index)
			cfg.Connections[index].Label = fmt.Sprintf("Fixture %d", index)
		}
	}
	return cfg
}

func seedAttachments(t *testing.T, dir, stem string, count int) {
	t.Helper()
	for index := 1; index <= count; index++ {
		name := stem + ".txt"
		if index > 1 {
			name = fmt.Sprintf("%s (%d).txt", stem, index)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strconv.Itoa(index)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
func writeLines(t *testing.T, path string, count int) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for index := 0; index < count; index++ {
		_, _ = fmt.Fprintf(file, "diagnostic line %d fixture words\n", index)
	}
}
