package web

import (
	"context"
	"encoding/hex"
	"log"

	"harness/internal/broker"
	"harness/internal/events"
	"harness/internal/projection"
)

// Item 2o7: THE ASSEMBLY. 2kq built the session client, the dispatcher and the push
// boxes and wired none of them, so a paired phone's every request timed out. While a
// pairing exists, one authenticated broker session is held with the existing client:
// every request is answered by DispatchAppMessage (the same handlers the page calls),
// and the device receives the same snapshot, patches and global events the tailnet
// client does.
//
// Item 2ok restored the three push hooks after the public broker accepted the
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
	c.mu.Unlock()
	if server == nil {
		return
	}
	device := "broker:" + hex.EncodeToString(pairing.DeviceKeyID)
	ctx, stop := context.WithCancel(context.Background())
	var client *broker.Client
	client = broker.NewClient(c.identity, pairing, c.dial, func(_ []byte, plaintext []byte) []byte {
		if c.deliverMirrorResponse(plaintext) {
			return nil
		}
		answer := server.DispatchAppMessage(device, plaintext)
		parts, err := appSplit(answer, appUnitMax)
		if err != nil || len(parts) == 1 {
			return answer
		}
		// A response over the unit budget goes as parts, like any downstream unit.
		for _, part := range parts {
			if err := client.Deliver(part); err != nil {
				log.Printf("broker: a response part was not sent: %v", err)
			}
		}
		return nil
	})
	client.OnSessionEvent(func(message string) {
		c.recordSession(pairing, message)
		server.app.Link(message) // item 2q7 (c): its shape only
	})
	client.OnHolding(func(connection context.Context) {
		server.streamPushes(connection, client)
	})
	client.OnConnected(func(connection context.Context) {
		c.mu.Lock()
		c.lastRefusal = ""
		if c.deviceSnapshots == nil {
			c.deviceSnapshots = map[string]projection.Snapshot{}
		}
		resume := c.deviceSnapshots
		c.mu.Unlock()
		server.streamUnitsToDeviceResuming(connection, client, resume)
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

var pushKinds = map[string]string{
	events.ApprovalRequired: "approval_required",
	events.RunStopped:       "run_stopped",
	events.ItemStuck:        "item_stuck",
}

// streamToDevice is the tailnet client's view, unit by unit: a snapshot of every
// session on connect, then its projection patches and the global durable events,
// until the connection ends. A device that sees a gap resyncs by cursor.
// deviceSink is the part of the broker client the stream uses.
type deviceSink interface {
	Deliver(plaintext []byte) error
	Notify(kind, chatID, notice string) error
}

func (s *Server) streamToDevice(ctx context.Context, client deviceSink) {
	s.streamToDeviceWithPushes(ctx, client, true, nil)
}

func (s *Server) streamUnitsToDevice(ctx context.Context, client deviceSink) {
	s.streamToDeviceWithPushes(ctx, client, false, nil)
}

func (s *Server) streamUnitsToDeviceResuming(ctx context.Context, client deviceSink, resume map[string]projection.Snapshot) {
	s.streamToDeviceWithPushes(ctx, client, false, resume)
}

func (s *Server) streamToDeviceWithPushes(ctx context.Context, client deviceSink, includePushes bool, resume map[string]projection.Snapshot) {
	raw, unsubscribeRaw := s.bus.Subscribe()
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
		// Item 2q7 (c): what one phone join sent (2pv measured 35.1 MB).
		if s.app != nil {
			s.app.NoteJoin(sent)
		}
		patches = subscribed
	}
	for {
		select {
		case patch, ok := <-patches:
			if !ok {
				return
			}
			send(map[string]any{"v": 1, "kind": "patch", "data": patch})
			if current, found := s.projector.CurrentSnapshot()[patch.SessionID]; found {
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
			data, _ := event.Data.(map[string]any)
			if kind, wake := pushKinds[event.Type]; includePushes && wake && data["notification_suppressed"] != true {
				if err := client.Notify(kind, event.SessionID, pushNotice(event)); err != nil {
					log.Printf("broker: the %s push was not sent: %v", kind, err)
				}
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

func (s *Server) streamPushes(ctx context.Context, client deviceSink) {
	raw, unsubscribe := s.bus.Subscribe()
	defer unsubscribe()
	for {
		select {
		case event, ok := <-raw:
			if !ok {
				return
			}
			data, _ := event.Data.(map[string]any)
			if kind, wake := pushKinds[event.Type]; wake && data["notification_suppressed"] != true {
				if err := client.Notify(kind, event.SessionID, pushNotice(event)); err != nil {
					log.Printf("broker: the %s push was not sent: %v", kind, err)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func pushNotice(event events.Event) string {
	if data, ok := event.Data.(map[string]any); ok {
		if name, ok := data["scheduled_job"].(string); ok && name != "" {
			if data["reason"] != "done" {
				return "Scheduled job " + name + " failed"
			}
			return "Scheduled job " + name + " finished"
		}
	}
	switch event.Type {
	case events.ApprovalRequired:
		return "Agent_b is waiting for your approval"
	case events.RunStopped:
		return "Agent_b stopped"
	default:
		return "An item is stuck"
	}
}
