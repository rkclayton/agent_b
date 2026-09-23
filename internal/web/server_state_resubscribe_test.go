package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
)

type controlledSSEWriter struct {
	header  http.Header
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
	mu      sync.Mutex
	body    bytes.Buffer
}

func (w *controlledSSEWriter) Header() http.Header { return w.header }
func (*controlledSSEWriter) WriteHeader(int)       {}
func (*controlledSSEWriter) Flush()                {}
func (w *controlledSSEWriter) Write(value []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	if w.release != nil {
		<-w.release
		w.release = nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(value)
}
func (w *controlledSSEWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.body.Bytes()...)
}

func TestSSEOverflowClosesAndNextSubscriptionStartsWithSnapshot(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	bus := events.NewBus()
	server := New(&cfg, "", t.TempDir(), RuntimeRoots{Application: t.TempDir(), Data: t.TempDir(), Workspace: t.TempDir()}, bus)

	release := make(chan struct{})
	first := &controlledSSEWriter{header: make(http.Header), entered: make(chan struct{}), release: release}
	firstDone := make(chan struct{})
	go func() {
		server.sse(first, httptest.NewRequest(http.MethodGet, "/api/events", nil))
		close(firstDone)
	}()
	<-first.entered
	for index := 0; index < 130; index++ {
		bus.Publish(events.New(events.ModelDelta, "", "run", map[string]any{"text": "x"}))
	}
	close(release)
	select {
	case <-firstDone:
	case <-time.After(3 * time.Second):
		t.Fatal("overflowed SSE subscription did not close")
	}

	ctx, cancel := context.WithCancel(context.Background())
	second := &controlledSSEWriter{header: make(http.Header), entered: make(chan struct{})}
	secondDone := make(chan struct{})
	go func() {
		server.sse(second, httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx))
		close(secondDone)
	}()
	<-second.entered
	cancel()
	<-secondDone
	if !bytes.Contains(second.Bytes(), []byte("event: snapshot")) {
		t.Fatalf("resubscribed stream did not start with a snapshot: %q", second.Bytes())
	}
}
