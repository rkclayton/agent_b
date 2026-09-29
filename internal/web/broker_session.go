package web

import (
	"context"
	"encoding/hex"
	"log"

	"harness/internal/broker"
	"harness/internal/projection"
)

// Item 2o7: THE ASSEMBLY. 2kq built the session client, the dispatcher and the push
// boxes and wired none of them, so a paired phone's every request timed out. While a
// pairing exists, one authenticated broker session is held with the existing client:
// every request is answered by DispatchAppMessage (the same handlers the page calls),
// and the device receives the same snapshot, patches and global events the tailnet
// client does.
//
// NO PUSHES YET. The live broker refused the push frame this client seals as
// "malformed (frame rejected)", and that refusal is fatal to the whole session, so a
// push would cost the phone its connection at the end of every run. The frame's shape
// belongs to the broker repository; until it is confirmed there, nothing is pushed.
//
// It lives as long as the pairing does IN THIS PROCESS. The pairing and this launch's
// identity are held in memory only (the security decision recorded at the top of
// broker_host.go), so after a restart there is nothing to reconnect with and the phone
// pairs again; a session is never opened without a pairing (2nu).

// attach gives the client the server whose handlers answer the phone.
func (c *BrokerClient) attach(server *Server) {
	c.mu.Lock()
	c.server = server
	c.mu.Unlock()
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
	client.OnConnected(func(connection context.Context) { server.streamToDevice(connection, client) })
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

// streamToDevice is the tailnet client's view, unit by unit: a snapshot of every
// session on connect, then its projection patches and the global durable events,
// until the connection ends. A device that sees a gap resyncs by cursor.
// deviceSink is the part of the broker client the stream uses.
type deviceSink interface {
	Deliver(plaintext []byte) error
}

func (s *Server) streamToDevice(ctx context.Context, client deviceSink) {
	raw, unsubscribeRaw := s.bus.Subscribe()
	defer unsubscribeRaw()
	send := func(unit map[string]any) {
		encoded, err := appCanonical(unit)
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
	if s.projector != nil && s.writers != nil {
		sessions, subscribed, unsubscribe, err := s.projector.SubscribeSnapshot(s.writers.SessionCursors())
		if err != nil {
			log.Printf("broker: the projection could not be subscribed: %v", err)
			return
		}
		defer unsubscribe()
		for id, snapshot := range sessions {
			send(map[string]any{"v": 1, "kind": "snapshot", "session_id": id, "data": snapshot})
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
		case event, ok := <-raw:
			if !ok {
				return
			}
			if event.SessionID == "" {
				send(map[string]any{"v": 1, "kind": "event", "data": event})
			}
		case <-ctx.Done():
			return
		}
	}
}
