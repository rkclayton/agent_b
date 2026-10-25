package web

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log"
	"sync"

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
	send := func(unit map[string]any) {
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
	}
	var patches <-chan projection.Patch
	var liveChanged <-chan struct{}
	if live != nil {
		liveChanged = live.changed
	}
	activities := map[string]string{}
	if s.projector != nil && s.writers != nil {
		cursors := s.writers.SessionCursors()
		sessions, subscribed, unsubscribe, err := s.projector.SubscribeSnapshot(cursors)
		if err != nil {
			log.Printf("broker: the projection could not be subscribed: %v", err)
			return
		}
		defer unsubscribe()
		folders := s.decorateChatList(sessions)
		send(map[string]any{"v": 1, "kind": "event", "data": events.New(events.ChatListSnapshot, "", "", map[string]any{"folders": folders})})
		activities = make(map[string]string, len(sessions))
		for id, snapshot := range sessions {
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
			if live != nil && live.opened() && found {
				if row := phoneChatListPatch(current, patch); row != nil {
					send(map[string]any{"v": 1, "kind": "event", "data": events.New(events.ChatListPatch, "", "", row)})
				}
			}
			if live == nil || live.accept(patch) {
				send(map[string]any{"v": 1, "kind": "patch", "data": patch})
			}
			if found {
				activity := latestTurnActivityPatch(current, patch)
				if activity != "" && activity != activities[patch.SessionID] {
					send(map[string]any{"v": 1, "kind": "event", "data": events.New(events.ChatListPatch, "", "", map[string]any{"operation": "activity", "session_id": patch.SessionID, "last_activity": activity})})
					activities[patch.SessionID] = activity
				}
			}
			if resume != nil {
				if current, ok := s.projector.CurrentSnapshot()[patch.SessionID]; ok {
					resume[patch.SessionID] = s.firstScreenSessions(map[string]projection.Snapshot{patch.SessionID: current}, "-")[patch.SessionID]
				}
			}
		case event, ok := <-raw:
			if !ok {
				return
			}
			if unit, mirrored := s.outboundMirror(event); mirrored {
				send(unit)
			}
			if event.SessionID == "" {
				send(map[string]any{"v": 1, "kind": "event", "data": event})
			}
		case <-ctx.Done():
			return
		}
	}
}

func phoneChatListPatch(current projection.Snapshot, patch projection.Patch) map[string]any {
	listChange := false
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
