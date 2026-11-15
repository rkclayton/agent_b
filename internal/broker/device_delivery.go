package broker

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"sync"
	"time"
)

// DeviceDelivery is a retained send, including one whose first wire write failed. Done
// closes exactly once; Outcome then remains delivered, expired, or dropped. The
// handle contains no message content. Only the caller retains completed handles.
type DeviceDelivery struct {
	id      string
	done    chan struct{}
	mu      sync.Mutex
	outcome string
}

func (h *DeviceDelivery) ID() string            { return h.id }
func (h *DeviceDelivery) Done() <-chan struct{} { return h.done }
func (h *DeviceDelivery) Outcome() string       { h.mu.Lock(); defer h.mu.Unlock(); return h.outcome }
func (h *DeviceDelivery) end(outcome string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.outcome == "" {
		h.outcome = outcome
		close(h.done)
	}
}

// Retained returns the at-most-64 handles still eligible for retransmission.
func (d *DeviceClient) Retained() []*DeviceDelivery {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*DeviceDelivery, 0, len(d.pending))
	for _, p := range d.pending {
		if p.delivery != nil {
			out = append(out, p.delivery)
		}
	}
	return out
}

func (d *DeviceClient) endLocked(id, outcome string) {
	if p, ok := d.pending[id]; ok {
		p.delivery.end(outcome)
		delete(d.pending, id)
		if d.stats != nil {
			delete(d.stats.sentAt, id)
		}
		<-d.slots
	}
}

// CloseAndDrop is final shutdown/unpairing. Close alone remains the legacy
// reconnect operation and retains sends. Call this before discarding the client;
// subsequent Connect is allowed, but none of these sends will be retransmitted.
func (d *DeviceClient) CloseAndDrop() {
	d.sendMu.Lock()
	d.mu.Lock()
	t, stop := d.link, d.stop
	d.session = nil
	if stop != nil {
		stop()
	}
	for id := range d.pending {
		d.endLocked(id, "dropped")
	}
	d.mu.Unlock()
	if t != nil {
		_ = t.Close(1000, "closed")
	}
	d.sendMu.Unlock()
}

var deviceClientName = regexp.MustCompile(`^[a-z]+/[0-9A-Za-z.+-]+$`)

// SetClient sets the next authenticated HELLO's optional client. The broker
// document bounds it to 32 ASCII bytes, <os>/<version>. Empty preserves the
// original bytes. Pairing itself starts with PAIR_BEGIN, never HELLO; the first
// Connect after ConfirmPair uses this value too. It contains no device/user name.
func (d *DeviceClient) SetClient(name string) error {
	if name != "" && (len(name) > 32 || !deviceClientName.MatchString(name)) {
		return errors.New("broker: client must be <os>/<version>, at most 32 ASCII bytes")
	}
	d.mu.Lock()
	d.client = name
	d.mu.Unlock()
	return nil
}

// OnTelemetry installs a content-free link.session/link.handshake observer.
// Callbacks run outside the client lock and must return promptly. No telemetry
// is transmitted by this client. Optional device lifecycle fields belong to the
// caller. ACK p50 is measured over the latest 64 acknowledgements of the session.
func (d *DeviceClient) OnTelemetry(observe func(string, map[string]any)) {
	d.mu.Lock()
	d.telemetry = observe
	d.mu.Unlock()
}
func (d *DeviceClient) observe(kind string, fields map[string]any) {
	d.mu.Lock()
	observe := d.telemetry
	d.mu.Unlock()
	if observe != nil {
		observe(kind, fields)
	}
}
func (d *DeviceClient) reportHandshake(step string, started time.Time, err error) {
	var failure *handshakeFailure
	if errors.As(err, &failure) {
		step = failure.step
	}
	d.observe("link.handshake", map[string]any{"step": step, "elapsed_ms": max(int64(0), d.now().Sub(started).Milliseconds())})
}

type deviceSessionStats struct {
	started                              time.Time
	sent, acked, received, receivedAcked int64
	sentAt                               map[string]time.Time
	latencies                            [64]int64
	samples                              int64
	ackMax                               int64
}

func (d *DeviceClient) ackLocked(id string) {
	s := d.stats
	if s == nil {
		return
	}
	if at, ok := s.sentAt[id]; ok {
		ms := max(int64(0), d.now().Sub(at).Milliseconds())
		s.latencies[s.samples%64] = ms
		s.samples++
		s.acked++
		s.ackMax = max(s.ackMax, ms)
		delete(s.sentAt, id)
	}
}
func (d *DeviceClient) sessionRecordLocked(reason string) map[string]any {
	s := d.stats
	d.stats = nil
	if s == nil {
		return nil
	}
	values := append([]int64(nil), s.latencies[:min(s.samples, 64)]...)
	slices.Sort(values)
	p50 := int64(0)
	if len(values) > 0 {
		p50 = values[(len(values)-1)/2]
	}
	return map[string]any{"role": "device", "sent": s.sent, "acked": s.acked, "unacked": s.sent - s.acked, "received": s.received, "received_unacked": s.received - s.receivedAcked, "ack_p50_ms": p50, "ack_max_ms": s.ackMax, "age_s": max(int64(0), int64(d.now().Sub(s.started)/time.Second)), "close": reason}
}

// reportConnectFailure preserves a TLS classification supplied by Dial.
func (d *DeviceClient) dialDevice(ctx context.Context, dial Dialer) (Transport, error) {
	start := d.now()
	t, err := dial(ctx)
	if err != nil {
		d.reportHandshake("dial", start, err)
	}
	return t, err
}
