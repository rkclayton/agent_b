package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/llm"
)

// Item 2px (e): ONE TRUE STATE per connection, and the only source of every lamp
// and word — the Settings header, its form, the chat header and the chat's
// connection picker. Every lamp used to be derived from whether a Test EVER
// passed, so a server that was off read "ready" and one that was fine read amber.
//
// A check lists models and reads the window; it never sends a completion and never
// loads a model (the HOMEPC rule). It runs at start, on Save, every minute while
// the app is open, and a real request's outcome sets it too.
type connectionHealth struct {
	Lamp   string `json:"lamp"` // ready, amber, alarm, or "" while checking
	Word   string `json:"word"`
	Window int    `json:"window,omitempty"`
}

const connectionHealthEvery = time.Minute

// StartConnectionHealth checks every connection now and then every minute until
// ctx ends.
func (s *Server) StartConnectionHealth(ctx context.Context) {
	every := s.healthEvery
	if every <= 0 {
		every = connectionHealthEvery
	}
	changes, unsubscribe := s.bus.Subscribe()
	go func() {
		defer unsubscribe()
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			for _, connection := range s.ConfigSnapshot().Connections {
				s.checkConnectionHealth(ctx, connection)
			}
		wait:
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					break wait
				case event, ok := <-changes:
					// A Save is a config change; anything else is not a reason to check.
					if !ok {
						return
					}
					if event.Type == events.ConfigChanged {
						break wait
					}
				}
			}
		}
	}()
}

func (s *Server) checkConnectionHealth(ctx context.Context, connection config.Connection) {
	check, cancel := context.WithTimeout(ctx, time.Duration(max(5, min(connection.RequestTimeoutS, 15)))*time.Second)
	defer cancel()
	client := llm.New(&connection)
	models, err := client.Models(check)
	health := connectionHealth{Lamp: "ready", Word: "ready"}
	switch {
	case err != nil:
		health = connectionHealth{Lamp: "alarm", Word: healthFailureWord(err)}
	case strings.TrimSpace(connection.Model) == "":
		health = connectionHealth{Lamp: "amber", Word: "no model chosen"}
	case !modelListed(connection.Model, models):
		health = connectionHealth{Lamp: "amber", Word: "model not listed"}
	}
	// Item 2px (i): the window the server gives ONE request. llama.cpp reports it
	// per slot; a connection set larger runs with the smaller and says so.
	if err == nil {
		if props, propsErr := client.Props(check); propsErr == nil && props.DefaultGenerationSettings.NCtx > 0 {
			health.Window = props.DefaultGenerationSettings.NCtx
			if s.runner != nil {
				s.runner.SetServerWindow(connection.ID, health.Window)
			}
			if connection.Context.NCtx > health.Window && health.Lamp == "ready" {
				health = connectionHealth{Lamp: "amber", Word: fmt.Sprintf("server allows only %d tokens", health.Window), Window: health.Window}
			}
		}
	}
	s.setConnectionHealth(connection.ID, health)
}

func healthFailureWord(err error) string {
	var shape *llm.ResponseShapeError
	if errors.As(err, &shape) {
		switch {
		case shape.Status == 401 || shape.Status == 403:
			return "key refused"
		case shape.Status >= 500 && shape.Status != 501:
			return "unreachable"
		}
		return "not a model server"
	}
	return "unreachable"
}

// setConnectionHealth stores a state and publishes it when it changed.
func (s *Server) setConnectionHealth(connectionID string, health connectionHealth) {
	s.healthMu.Lock()
	if s.health == nil {
		s.health = map[string]connectionHealth{}
	}
	changed := s.health[connectionID] != health
	s.health[connectionID] = health
	s.healthMu.Unlock()
	if changed && s.bus != nil {
		s.bus.Publish(events.New(events.ConnectionHealth, "", "", map[string]any{"connection_id": connectionID, "health": health}))
	}
}

func (s *Server) connectionHealthState() map[string]connectionHealth {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	out := make(map[string]connectionHealth, len(s.health))
	for id, health := range s.health {
		out[id] = health
	}
	return out
}
