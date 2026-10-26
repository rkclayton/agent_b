package web

import (
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"harness/internal/broker"
	"harness/internal/events"
	"harness/internal/projection"
)

// THE ASSEMBLY. 2kq built the session client, the dispatcher and the push
// boxes and wired none of them, so a paired phone's every request timed out. While a
// pairing exists, one authenticated broker session is held with the existing client:
// every request is answered by DispatchAppMessage (the same handlers the page calls),
// and the device receives the same snapshot, patches and global events the tailnet
// client does.
//
// restored the three push hooks after the public broker accepted the
// document-conformant four-field frame. Push-provider refusals are non-fatal and do
// not end this session.
//
// It lives as long as the stored pairing does. A restart loads the identity and pairing,
// then attach starts this session after the real server is available; without a pairing
// no connection is opened (2nu, 2ob).

// attach gives the client the server whose handlers answer the phone.
func (c *BrokerClient) attach(server *Server) {
	c.mu.Lock()
	c.server = server
	var pairing *broker.Pairing
	if c.pairing != nil && c.client == nil {
		copy := *c.pairing
		pairing = &copy
	}
	c.mu.Unlock()
	if pairing != nil {
		c.startSession(*pairing)
	}
}

// startSession runs the paired session until revoke stops it.
func (c *BrokerClient) startSession(pairing broker.Pairing) {
	c.mu.Lock()
	server := c.server
	if server == nil {
		c.mu.Unlock()
		return
	}
	c.stopSessionLocked()
	c.mu.Unlock()
	device := "broker:" + hex.EncodeToString(pairing.DeviceKeyID)
	ctx, stop := context.WithCancel(context.Background())
	var client *broker.Client
	live := newPhoneStreamState()
	client = broker.NewClient(c.identity, pairing, c.dial, func(_ []byte, plaintext []byte) []byte {
		if c.deliverMirrorResponse(plaintext) {
			return nil
		}
		answer := server.dispatchAppMessage(device, plaintext, live)
		var request appRequestUnit
		_ = json.Unmarshal(plaintext, &request)
		return deliverPhoneAnswer(handlerSink{client}, server, request.ID, answer, live)
	})
	client.OnSessionEvent(func(message string) {
		c.recordSession(pairing, message)
		server.app.Link(message) // its shape only
	})
	client.OnConnected(func(connection context.Context) {
		live.reset()
		c.mu.Lock()
		c.lastRefusal = ""
		if c.deviceSnapshots == nil {
			c.deviceSnapshots = map[string]projection.Snapshot{}
		}
		resume := c.deviceSnapshots
		c.mu.Unlock()
		server.streamUnitsToDeviceOpening(connection, client, resume, live)
	})
	client.OnRefused(func(code, detail string) {
		outcome := "refused by the broker — " + code + ": " + detail
		c.recordPairing("session refusal " + code + ": " + detail)
		c.recordPairing("session outcome " + outcome)
		c.mu.Lock()
		c.lastRefusal = outcome
		c.mu.Unlock()
	})
	c.mu.Lock()
	c.client, c.stopSession = client, stop
	c.mu.Unlock()
	go func() {
		if err := client.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("broker: the paired session ended: %v", err)
		}
	}()
}

// stopSessionLocked ends the session; the caller holds c.mu.
func (c *BrokerClient) stopSessionLocked() {
	if c.stopSession != nil {
		c.stopSession()
	}
	c.client, c.stopSession = nil, nil
}

// deviceStreamTick is how long a device's streamed text may wait to be merged
// with the next tokens (item 2wu): ten frames a second instead of one per
// token, at most one tick behind the PC, whose own stream is not merged.
const deviceStreamTick = 100 * time.Millisecond

// deviceSessionlessEvents is the one list of session-less events a device is
// sent (item 2wt); docs/app-message-v1.md quotes it and a test holds them
// equal. Everything else — config.changed first — stays on the PC.
var deviceSessionlessEvents = []string{
	events.AgentTools,       // 2ws: an agent's tools after a toggle
	events.ChatDeleted,      // the iOS chat list removes the row
	events.ChatListPatch,    // the iOS chat list's folders
	events.ChatListSnapshot, // the iOS chat list's folders
}

// deviceListPage is how many chats a device's list is sent at once besides the
// running and waiting ones (item 2wy): a phone's first screen and several scrolls,
// a few hundred rows where a PC keeps thousands; chat.list.page sends the rest.
const deviceListPage = 200

type deviceListRow struct {
	id string
	at time.Time
}

// deviceListOrder is the list's order: newest activity first, ties by id.
func deviceListOrder(a, b deviceListRow) int {
	return cmp.Or(b.at.Compare(a.at), strings.Compare(b.id, a.id))
}

// deviceChatPage is one page of a device's chat list. The first (before "") holds
// every running or waiting chat (the phones' Running and Waiting sections), keep,
// and the n newest others; a later one the n others after the cursor before. next
// is the cursor after its last row, "" when none is left; ok is false for a cursor
// this desktop did not give.
func deviceChatPage(sessions map[string]projection.Snapshot, keep, before string, n int) (page map[string]projection.Snapshot, next string, ok bool) {
	var after *deviceListRow
	if before != "" {
		at, id, _ := strings.Cut(before, "|")
		parsed, err := time.Parse(time.RFC3339Nano, at)
		if err != nil || id == "" {
			return nil, "", false
		}
		after = &deviceListRow{id, parsed}
	}
	page = map[string]projection.Snapshot{}
	rows := make([]deviceListRow, 0, len(sessions))
	for id, value := range sessions {
		if value.PendingApproval != nil || slices.Contains([]string{"running", "queued", "stopping", "paused"}, value.Run.Status) || id == keep {
			if after == nil {
				page[id] = value
			}
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, cmp.Or(latestChatActivity(value), value.CreatedAt))
		rows = append(rows, deviceListRow{id, at})
	}
	slices.SortFunc(rows, deviceListOrder)
	start := 0
	if after != nil {
		at, found := slices.BinarySearchFunc(rows, *after, deviceListOrder)
		start = at
		if found {
			start++
		}
	}
	end := min(len(rows), start+n)
	for _, row := range rows[start:end] {
		page[row.id] = sessions[row.id]
	}
	if end < len(rows) {
		next = rows[end-1].at.UTC().Format(time.RFC3339Nano) + "|" + rows[end-1].id
	}
	return page, next, true
}

// deviceListed is which chats a device's list holds: the join's page, the pages it
// asked for, and each chat an upsert has since sent it (item 2wy).
type deviceListed struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (l *deviceListed) set(page map[string]projection.Snapshot) {
	l.mu.Lock()
	l.ids = make(map[string]bool, len(page))
	for id := range page {
		l.ids[id] = true
	}
	l.mu.Unlock()
}

// add lists id and says whether the device lacked it.
func (l *deviceListed) add(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ids == nil {
		l.ids = map[string]bool{}
	}
	lacked := !l.ids[id]
	l.ids[id] = true
	return lacked
}

func listOrNone(next string) any {
	if next == "" {
		return nil
	}
	return next
}

func (s *Server) noteDeviceDropped(eventType string) {
	s.deviceDroppedMu.Lock()
	if s.deviceDropped == nil {
		s.deviceDropped = map[string]int64{}
	}
	s.deviceDropped[eventType]++
	s.deviceDroppedMu.Unlock()
}

func (s *Server) deviceEventsDropped() map[string]int64 {
	s.deviceDroppedMu.Lock()
	defer s.deviceDroppedMu.Unlock()
	return maps.Clone(s.deviceDropped)
}

// deviceTextPatch says a patch is one streamed token: text appended to one
// chat entry, with only the stream's running state beside it.
func deviceTextPatch(patch projection.Patch) bool {
	text := false
	for _, operation := range patch.Operations {
		switch {
		case operation.Op == "append" && strings.HasPrefix(operation.Path, "/chat/") && (strings.HasSuffix(operation.Path, "/text") || strings.HasSuffix(operation.Path, "/reasoning")):
			text = true
		case operation.Op == "append" && (operation.Path == "/run/partial" || operation.Path == "/timeline"):
		case operation.Op == "replace" && operation.Path == "/activity":
		default:
			return false
		}
	}
	return text && !patch.Transient
}

// deviceTextBatch merges consecutive token patches into one patch of the same
// shape: string appends joined, /activity's last value, other /timeline appends kept.
// A token's own model.delta timeline entry is left out: neither phone reads one, and
// they were most of a streamed reply's bytes (item 2wv).
type deviceTextBatch struct {
	patch projection.Patch
	runID string
	text  map[int]*strings.Builder
	index map[string]int
}

func (b *deviceTextBatch) empty() bool { return b.patch.SessionID == "" }

func (b *deviceTextBatch) add(patch projection.Patch, runID string) bool {
	if b.empty() {
		b.patch = projection.Patch{SchemaVersion: patch.SchemaVersion, SessionID: patch.SessionID, PreviousCursor: patch.PreviousCursor}
		b.runID, b.text, b.index = runID, map[int]*strings.Builder{}, map[string]int{}
	} else if patch.SessionID != b.patch.SessionID || patch.PreviousCursor != b.patch.Cursor {
		return false
	}
	for _, operation := range patch.Operations {
		at, merged := b.index[operation.Path]
		switch {
		case operation.Path == "/timeline":
			var entry struct{ Type string }
			if json.Unmarshal(operation.Value, &entry) == nil && entry.Type == events.ModelDelta {
				continue
			}
			b.patch.Operations = append(b.patch.Operations, operation)
		case merged && operation.Op == "replace":
			b.patch.Operations[at].Value = operation.Value
		case operation.Op == "replace":
			b.index[operation.Path] = len(b.patch.Operations)
			b.patch.Operations = append(b.patch.Operations, operation)
		default:
			var value string
			_ = json.Unmarshal(operation.Value, &value)
			if !merged {
				at = len(b.patch.Operations)
				b.index[operation.Path], b.text[at] = at, &strings.Builder{}
				b.patch.Operations = append(b.patch.Operations, projection.Operation{Op: "append", Path: operation.Path})
			}
			b.text[at].WriteString(value)
		}
	}
	b.patch.Cursor = patch.Cursor
	return true
}

func (b *deviceTextBatch) take() projection.Patch {
	patch := b.patch
	for at, text := range b.text {
		patch.Operations[at].Value, _ = json.Marshal(text.String())
	}
	*b = deviceTextBatch{}
	return patch
}

// deviceStreamRuns keeps the last runs' device stream counts for diagnostics.
const deviceStreamRuns = 16

type deviceStreamCount struct {
	Session string `json:"session_id"`
	Run     string `json:"run_id"`
	Patches int64  `json:"patches"`
	Frames  int64  `json:"frames"`
	Bytes   int64  `json:"bytes"`
}

func (s *Server) noteDeviceStream(session, run string, patches int64, bytes int) {
	s.deviceDroppedMu.Lock()
	defer s.deviceDroppedMu.Unlock()
	at := slices.IndexFunc(s.deviceStream, func(count deviceStreamCount) bool { return count.Session == session && count.Run == run })
	if at < 0 {
		if len(s.deviceStream) == deviceStreamRuns {
			s.deviceStream = slices.Delete(s.deviceStream, 0, 1)
		}
		s.deviceStream = append(s.deviceStream, deviceStreamCount{Session: session, Run: run})
		at = len(s.deviceStream) - 1
	}
	s.deviceStream[at].Patches += patches
	if bytes > 0 {
		s.deviceStream[at].Frames++
		s.deviceStream[at].Bytes += int64(bytes)
	}
}

func (s *Server) deviceStreamCounts() []deviceStreamCount {
	s.deviceDroppedMu.Lock()
	defer s.deviceDroppedMu.Unlock()
	return slices.Clone(s.deviceStream)
}

// streamToDevice sends the bounded join, global events, and either the legacy
// all-chat patch stream or the chat.open selection. A gap resyncs by cursor.
type deviceSink interface {
	Deliver(plaintext []byte) error
}

// handlerSink sends an answer's parts from the handler, which runs on the client's
// reader: Deliver's wait for an ACK would wait on that reader (item 2wq).
type handlerSink struct{ client *broker.Client }

func (s handlerSink) Deliver(plaintext []byte) error { return s.client.DeliverNow(plaintext) }

func deliverPhoneAnswer(client deviceSink, server *Server, requestID string, answer []byte, live *phoneStreamState) []byte {
	prepared := live.preparedAnswer()
	parts, err := appSplit(answer, appUnitMax)
	if err != nil {
		if prepared {
			live.cancelPrepared()
		}
		return answer
	}
	if len(parts) == 1 && !prepared {
		return answer
	}
	for _, part := range parts {
		if err = client.Deliver(part); err != nil {
			if prepared {
				live.cancelPrepared()
			}
			log.Printf("broker: request %q response delivery failed: %v", requestID, err)
			return server.appProblem(requestID, 503, "response delivery failed: "+err.Error())
		}
	}
	if prepared {
		live.activate()
	}
	return nil
}

type phoneStreamState struct {
	mu       sync.Mutex
	epoch    uint64
	mode     byte // zero: released phone; one: answer in progress; two: one live chat
	session  string
	cursor   projection.Cursor
	prepared bool
	held     []projection.Patch
	changed  chan struct{}
	listed   deviceListed
}

func newPhoneStreamState() *phoneStreamState {
	return &phoneStreamState{changed: make(chan struct{}, 1)}
}
func (p *phoneStreamState) reset() {
	p.mu.Lock()
	p.epoch++
	p.mode, p.session, p.cursor, p.prepared, p.held = 0, "", projection.Cursor{}, false, nil
	p.mu.Unlock()
}
func (p *phoneStreamState) begin(session string) uint64 {
	p.mu.Lock()
	p.epoch++
	p.mode, p.session, p.cursor, p.prepared, p.held = 1, session, projection.Cursor{}, false, nil
	epoch := p.epoch
	p.mu.Unlock()
	return epoch
}
func (p *phoneStreamState) prepare(epoch uint64, cursor projection.Cursor) {
	p.mu.Lock()
	if p.epoch == epoch && p.mode == 1 {
		p.cursor, p.prepared = cursor, true
	}
	p.mu.Unlock()
}
func (p *phoneStreamState) abortUnlessPrepared(epoch uint64) {
	p.mu.Lock()
	if p.epoch == epoch && !p.prepared {
		p.mode, p.held = 0, nil
	}
	p.mu.Unlock()
}
func (p *phoneStreamState) preparedAnswer() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prepared && p.mode == 1
}
func (p *phoneStreamState) opened() bool    { p.mu.Lock(); defer p.mu.Unlock(); return p.mode != 0 }
func (p *phoneStreamState) cancelPrepared() { p.reset() }
func (p *phoneStreamState) activate() {
	p.mu.Lock()
	if p.prepared && p.mode == 1 {
		p.mode, p.prepared = 2, false
		select {
		case p.changed <- struct{}{}:
		default:
		}
	}
	p.mu.Unlock()
}
func (p *phoneStreamState) accept(patch projection.Patch) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode == 0 {
		return true
	}
	if patch.SessionID != p.session || p.session == "" {
		return false
	}
	if p.mode == 1 || len(p.held) > 0 {
		p.held = append(p.held, patch)
		return false
	}
	if !patch.Transient && patch.Cursor.Generation == p.cursor.Generation && patch.Cursor.Offset <= p.cursor.Offset {
		return false
	}
	p.cursor = patch.Cursor
	return true
}
func (p *phoneStreamState) release() []projection.Patch {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode != 2 || len(p.held) == 0 {
		return nil
	}
	ready := make([]projection.Patch, 0, len(p.held))
	for _, patch := range p.held {
		if !patch.Transient && patch.Cursor.Generation == p.cursor.Generation && patch.Cursor.Offset <= p.cursor.Offset {
			continue
		}
		p.cursor, ready = patch.Cursor, append(ready, patch)
	}
	p.held = nil
	return ready
}

func (s *Server) streamToDevice(ctx context.Context, client deviceSink) {
	s.streamToDeviceWithPushes(ctx, client, nil, nil)
}

func (s *Server) streamUnitsToDevice(ctx context.Context, client deviceSink) {
	s.streamToDeviceWithPushes(ctx, client, nil, nil)
}

func (s *Server) streamUnitsToDeviceResuming(ctx context.Context, client deviceSink, resume map[string]projection.Snapshot) {
	s.streamToDeviceWithPushes(ctx, client, resume, nil)
}

func (s *Server) streamUnitsToDeviceOpening(ctx context.Context, client deviceSink, resume map[string]projection.Snapshot, live *phoneStreamState) {
	s.streamToDeviceWithPushes(ctx, client, resume, live)
}

func (s *Server) streamToDeviceWithPushes(ctx context.Context, client deviceSink, resume map[string]projection.Snapshot, live *phoneStreamState) {
	raw, dropped, unsubscribeRaw := s.bus.ListenUntilDrop("phone")
	defer unsubscribeRaw()
	var sent int64
	deliver := func(unit map[string]any) int {
		encoded, err := appCanonical(unit)
		sent += int64(len(encoded))
		if err == nil {
			var parts [][]byte
			if parts, err = appSplit(encoded, appUnitMax); err == nil {
				for _, part := range parts {
					if err = client.Deliver(part); err != nil {
						break
					}
				}
			}
		}
		if err != nil {
			log.Printf("broker: a %v unit was not sent: %v", unit["kind"], err)
		}
		return len(encoded)
	}
	// Streamed text waits at most one tick to be merged with the tokens after
	// it; anything else sent flushes it first, so nothing reorders (item 2wu).
	var batch deviceTextBatch
	var tick *time.Timer
	var ticked <-chan time.Time
	flush := func() {
		if batch.empty() {
			return
		}
		runID := batch.runID
		patch := batch.take()
		s.noteDeviceStream(patch.SessionID, runID, 0, deliver(map[string]any{"v": 1, "kind": "patch", "data": patch}))
		tick.Stop()
		ticked = nil
		if resume != nil {
			if current, ok := s.projector.CurrentSnapshot()[patch.SessionID]; ok {
				resume[patch.SessionID] = s.firstScreenSessions(map[string]projection.Snapshot{patch.SessionID: current}, "-")[patch.SessionID]
			}
		}
	}
	send := func(unit map[string]any) {
		flush()
		deliver(unit)
	}
	var patches <-chan projection.Patch
	var liveChanged <-chan struct{}
	if live != nil {
		liveChanged = live.changed
	}
	activities := map[string]string{}
	listed := &deviceListed{}
	if live != nil {
		listed = &live.listed
	}
	if s.projector != nil && s.writers != nil {
		cursors := s.writers.SessionCursors()
		sessions, subscribed, unsubscribe, err := s.projector.SubscribeSnapshot(cursors)
		if err != nil {
			log.Printf("broker: the projection could not be subscribed: %v", err)
			return
		}
		defer unsubscribe()
		folders := s.decorateChatList(sessions)
		page, next, _ := deviceChatPage(sessions, "", "", deviceListPage)
		listed.set(page)
		send(map[string]any{"v": 1, "kind": "event", "data": events.New(events.ChatListSnapshot, "", "", map[string]any{"folders": folders, "total": len(sessions), "next": listOrNone(next)})})
		activities = make(map[string]string, len(page))
		for id, snapshot := range page {
			activities[id] = snapshot.LastActivity
			bounded := s.firstScreenSessions(map[string]projection.Snapshot{id: snapshot}, "-")[id]
			previous, reconnect := resume[id]
			if reconnect && previous.Cursor.Generation == bounded.Cursor.Generation && previous.Cursor.Offset <= bounded.Cursor.Offset {
				if current, missed, tailErr := projection.ProjectTailPatches(cursors[id].Path, previous.Cursor.Offset, previous); tailErr == nil {
					for _, patch := range missed {
						send(map[string]any{"v": 1, "kind": "patch", "data": patch})
					}
					bounded.Cursor, previous.Cursor = current.Cursor, current.Cursor
				} else {
					reconnect = false
				}
			}
			if !reconnect {
				send(map[string]any{"v": 1, "kind": "snapshot", "session_id": id, "data": bounded})
			}
			if resume != nil {
				resume[id] = bounded
			}
		}
		// what one phone join sent (2pv measured 35.1 MB).
		if s.app != nil {
			s.app.NoteJoin(sent)
		}
		patches = subscribed
	}
	for {
		select {
		case <-liveChanged:
			for _, patch := range live.release() {
				send(map[string]any{"v": 1, "kind": "patch", "data": patch})
			}
		case <-dropped:
			return
		case patch, ok := <-patches:
			if !ok {
				return
			}
			current, found := s.projector.CurrentSnapshot()[patch.SessionID]
			// a chat the device's list lacks arrives as a row before its patch.
			if lacked := found && listed.add(patch.SessionID); lacked || found && live != nil && live.opened() {
				if row := phoneChatListPatch(current, patch, lacked); row != nil {
					send(map[string]any{"v": 1, "kind": "event", "data": events.New(events.ChatListPatch, "", "", row)})
				}
			}
			s.noteDeviceStream(patch.SessionID, current.Run.RunID, 1, 0)
			batched := false
			if live == nil || live.accept(patch) {
				if batched = deviceTextPatch(patch); batched {
					if !batch.add(patch, current.Run.RunID) {
						flush()
						batch.add(patch, current.Run.RunID)
					}
					if ticked == nil {
						if tick == nil {
							tick = time.NewTimer(deviceStreamTick)
						} else {
							tick.Reset(deviceStreamTick)
						}
						ticked = tick.C
					}
				} else {
					flush()
					s.noteDeviceStream(patch.SessionID, current.Run.RunID, 0, deliver(map[string]any{"v": 1, "kind": "patch", "data": patch}))
				}
			}
			if found {
				activity := latestTurnActivityPatch(current, patch)
				if activity != "" && activity != activities[patch.SessionID] {
					send(map[string]any{"v": 1, "kind": "event", "data": events.New(events.ChatListPatch, "", "", map[string]any{"operation": "activity", "session_id": patch.SessionID, "last_activity": activity})})
					activities[patch.SessionID] = activity
				}
			}
			if resume != nil && !batched {
				if current, ok := s.projector.CurrentSnapshot()[patch.SessionID]; ok {
					resume[patch.SessionID] = s.firstScreenSessions(map[string]projection.Snapshot{patch.SessionID: current}, "-")[patch.SessionID]
				}
			}
		case <-ticked:
			ticked = nil
			flush()
		case event, ok := <-raw:
			if !ok {
				return
			}
			if unit, mirrored := s.outboundMirror(event); mirrored {
				send(unit)
			}
			if event.SessionID == "" && slices.Contains(deviceSessionlessEvents, event.Type) {
				send(map[string]any{"v": 1, "kind": "event", "data": event})
			} else if event.SessionID == "" {
				s.noteDeviceDropped(event.Type)
			}
		case <-ctx.Done():
			return
		}
	}
}

func phoneChatListPatch(current projection.Snapshot, patch projection.Patch, listChange bool) map[string]any {
	for _, operation := range patch.Operations {
		switch operation.Path {
		case "/complete", "/id", "/label", "/agent_id", "/connection_id", "/agent_name", "/b_connection", "/role", "/created_at", "/run", "/queued_messages", "/runnable", "/not_runnable_reason", "/closed":
			listChange = true
		}
	}
	if !listChange {
		return nil
	}
	return map[string]any{"operation": "upsert", "session": map[string]any{
		"id": current.ID, "label": current.Label, "agent_id": current.AgentID,
		"connection_id": current.ConnectionID, "agent_name": current.AgentName,
		"b_connection": current.BConnection, "role": current.Role, "created_at": current.CreatedAt,
		"run": current.Run, "queued_messages": current.QueuedMessages, "runnable": current.Runnable,
		"not_runnable_reason": current.NotRunnableReason, "closed": current.Closed,
	}}
}
