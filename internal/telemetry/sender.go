package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Item 2jg (c) and (d). The numbers are docs/TELEMETRY.md's.
const (
	BatchInterval  = 5 * time.Minute
	BatchEventCap  = 200
	BatchByteCap   = 64 << 10
	BacklogMaxAge  = 24 * time.Hour
	queueDirectory = "telemetry"
)

const DefaultEndpoint = "https://broker.agentb.app/v1/telemetry"

// Event is one allow-listed event, ready to leave.
type Event struct {
	Type string         `json:"type"`
	At   string         `json:"at"`
	Data map[string]any `json:"-"`
}

// MarshalJSON flattens the picked fields beside type and at, which is the shape
// docs/TELEMETRY.md describes.
func (e Event) MarshalJSON() ([]byte, error) {
	flat := map[string]any{"type": e.Type, "at": e.At}
	for key, value := range e.Data {
		if key == "type" || key == "at" {
			continue
		}
		flat[key] = value
	}
	return json.Marshal(flat)
}

// Batch is what leaves the machine.
type Batch struct {
	Schema       int     `json:"schema"`
	InstallID    string  `json:"install_id"`
	SentAt       string  `json:"sent_at"`
	AgentVersion string  `json:"agent_version"`
	OS           string  `json:"os"`
	Events       []Event `json:"events"`
}

type Options struct {
	Endpoint     string
	InstallID    string
	AgentVersion string
	DataRoot     string
	Client       *http.Client
	Now          func() time.Time
	// Transport replaces the HTTP post in tests. It is the ONLY way a batch
	// leaves, so a test that asserts nothing left asserts against this.
	Transport func(context.Context, []byte) error
}

// Sender accumulates allow-listed events and sends them in batches.
type Sender struct {
	mu        sync.Mutex
	options   Options
	pending   []Event
	sentIndex []Record
	refused   int
	invalid   int
	stop      chan struct{}
	stopped   bool
	wg        sync.WaitGroup
}

// Record is one line of the "what was sent" view: the batch exactly as it left.
type Record struct {
	SentAt string `json:"sent_at"`
	Events int    `json:"events"`
	Bytes  int    `json:"bytes"`
	OK     bool   `json:"ok"`
	Body   string `json:"body"`
}

// RecordsKept is the "last 20 batches" of item 2jg (d).
const RecordsKept = 20

// NewInstallID is a random v4 UUID. A new one is issued whenever the switch goes
// off then on, so two runs of telemetry from one machine cannot be joined.
func NewInstallID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return ""
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// New starts a sender. It returns nil when there is nowhere to send, so the
// caller's "is telemetry running" question has one answer and not two.
func New(options Options) *Sender {
	if options.Endpoint == "" && options.Transport == nil {
		return nil
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Client == nil {
		options.Client = &http.Client{Timeout: 30 * time.Second}
	}
	sender := &Sender{options: options, stop: make(chan struct{})}
	sender.wg.Add(1)
	go sender.loop()
	return sender
}

// Observe folds one event. A type the allow-list has not classified is dropped
// here and caught by the suite, never sent on a guess.
func (s *Sender) Observe(eventType, at string, data map[string]any) bool {
	if s == nil {
		return false
	}
	class, known := Classify(eventType)
	if !known || !class.Sent {
		return false
	}
	picked := Pick(class, data)
	if picked == nil {
		s.mu.Lock()
		s.invalid++
		s.mu.Unlock()
		return false
	}
	s.mu.Lock()
	s.pending = append(s.pending, Event{Type: eventType, At: at, Data: picked})
	full := len(s.pending) >= BatchEventCap
	s.mu.Unlock()
	if full {
		s.Flush()
	}
	return true
}

// ReportByteCap is item 2pw (c)'s bound on one report.
const ReportByteCap = 48 << 10

// ReportOne sends one event in a batch of its own, now, whether or not the
// switch is on: item 2pw's "Report this chat", where the click is the consent
// for that one report. It passes the same allow-list and redaction as every
// batch, under an install id of its own so it cannot be joined to anything.
func ReportOne(options Options, eventType string, data map[string]any) ([]byte, error) {
	class, known := Classify(eventType)
	picked := Pick(class, data)
	if !known || picked == nil {
		return nil, fmt.Errorf("%s is not sendable", eventType)
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Client == nil {
		options.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if options.Endpoint == "" {
		options.Endpoint = DefaultEndpoint
	}
	options.InstallID = NewInstallID()
	sender := &Sender{options: options}
	body, err := json.Marshal(sender.batch([]Event{{Type: eventType, At: options.Now().UTC().Format(time.RFC3339), Data: picked}}))
	if err != nil {
		return nil, err
	}
	if len(body) > ReportByteCap {
		return nil, fmt.Errorf("report is %d bytes, over %d", len(body), ReportByteCap)
	}
	return body, sender.post(body)
}

func (s *Sender) InvalidDropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.invalid
}

func (s *Sender) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(BatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.Flush()
			s.drainQueue()
		}
	}
}

// Flush sends what has accumulated, splitting at the byte cap.
func (s *Sender) Flush() {
	if s == nil {
		return
	}
	s.mu.Lock()
	all := s.pending
	s.pending = nil
	s.mu.Unlock()
	// Item 2pw: the recorder's types travel in batches of their own. A receiver
	// that does not know them yet refuses the whole batch (422
	// unknown_event_type, 2026-10-04), and that must not take the older events
	// down with it.
	var older, recorded []Event
	for _, event := range all {
		if RecorderTypes[event.Type] {
			recorded = append(recorded, event)
		} else {
			older = append(older, event)
		}
	}
	s.send(older)
	s.send(recorded)
}

// RecorderTypes are the types docs/telemetry-trace.md specifies.
var RecorderTypes = map[string]bool{"trace": true, "run.summary": true}

func (s *Sender) send(pending []Event) {
	for len(pending) > 0 {
		take := len(pending)
		var body []byte
		for take > 0 {
			encoded, err := json.Marshal(s.batch(pending[:take]))
			if err != nil {
				return
			}
			if len(encoded) <= BatchByteCap || take == 1 {
				body = encoded
				break
			}
			take /= 2
		}
		s.deliver(body)
		pending = pending[take:]
	}
}

func (s *Sender) batch(events []Event) Batch {
	return Batch{
		Schema:       SchemaVersion,
		InstallID:    s.options.InstallID,
		SentAt:       s.options.Now().UTC().Format(time.RFC3339),
		AgentVersion: s.options.AgentVersion,
		OS:           "windows",
		Events:       events,
	}
}

// deliver posts one batch, or queues it when the receiver cannot be reached.
func (s *Sender) deliver(body []byte) {
	err := s.post(body)
	s.remember(Record{
		SentAt: s.options.Now().UTC().Format(time.RFC3339),
		Events: countEvents(body),
		Bytes:  len(body),
		OK:     err == nil,
		Body:   string(body),
	})
	if err != nil {
		var refused *receiverRefusal
		if errors.As(err, &refused) {
			s.mu.Lock()
			s.refused++
			total := s.refused
			s.mu.Unlock()
			log.Printf("telemetry: dropped receiver-refused batch (HTTP %d); refused total %d", refused.status, total)
			return
		}
		s.enqueue(body)
	}
}

type receiverRefusal struct{ status int }

func (e *receiverRefusal) Error() string { return fmt.Sprintf("receiver answered %d", e.status) }

func (s *Sender) post(body []byte) error {
	if s.options.Transport != nil {
		return s.options.Transport(context.Background(), body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.options.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.options.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 400 && response.StatusCode < 500 {
			return &receiverRefusal{status: response.StatusCode}
		}
		return fmt.Errorf("receiver answered %d", response.StatusCode)
	}
	return nil
}

func (s *Sender) Refused() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused
}

func countEvents(body []byte) int {
	var decoded struct {
		Events []json.RawMessage `json:"events"`
	}
	_ = json.Unmarshal(body, &decoded)
	return len(decoded.Events)
}

func (s *Sender) remember(record Record) {
	s.mu.Lock()
	s.sentIndex = append(s.sentIndex, record)
	if len(s.sentIndex) > RecordsKept {
		s.sentIndex = s.sentIndex[len(s.sentIndex)-RecordsKept:]
	}
	s.mu.Unlock()
}

// Records is the "what was sent" view, newest last.
func (s *Sender) Records() []Record {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.sentIndex...)
}

// ---------------------------------------------------------------- the backlog

func (s *Sender) queueDir() string {
	if s.options.DataRoot == "" {
		return ""
	}
	return filepath.Join(s.options.DataRoot, queueDirectory)
}

func (s *Sender) enqueue(body []byte) {
	dir := s.queueDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	name := fmt.Sprintf("%d.json", s.options.Now().UTC().UnixNano())
	_ = os.WriteFile(filepath.Join(dir, name), body, 0o600)
}

// drainQueue sends what is waiting, oldest first, and drops what has waited
// longer than the backlog allows — with a log line, because a silent drop of
// the operator's own diagnostics is the kind of thing this order exists to end.
func (s *Sender) drainQueue() {
	dir := s.queueDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	cutoff := s.options.Now().Add(-BacklogMaxAge)
	dropped := 0
	for _, name := range names {
		full := filepath.Join(dir, name)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(full)
			dropped++
			continue
		}
		body, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		if err := s.post(body); err != nil {
			var refused *receiverRefusal
			if !errors.As(err, &refused) {
				return
			} // still unreachable; the rest stays queued, in order
			s.mu.Lock()
			s.refused++
			total := s.refused
			s.mu.Unlock()
			log.Printf("telemetry: dropped queued receiver-refused batch (HTTP %d); refused total %d", refused.status, total)
		}
		_ = os.Remove(full)
	}
	if dropped > 0 {
		log.Printf("telemetry: dropped %d queued batch(es) older than %s", dropped, BacklogMaxAge)
	}
}

// Close stops the sender and DELETES the queue. Item 2jg (d): off does not mean
// "sends less". The caller drops its reference afterwards, so the subscriber is
// detached, nothing is collected, and there is nothing on disk to resume from.
func (s *Sender) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.pending = nil
	close(s.stop)
	s.mu.Unlock()
	s.wg.Wait()
	if dir := s.queueDir(); dir != "" {
		_ = os.RemoveAll(dir)
	}
}
