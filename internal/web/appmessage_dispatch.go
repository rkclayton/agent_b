package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
)

type pairedDeviceContextKey struct{}

func pairedDeviceAuthenticated(r *http.Request) bool {
	_, ok := r.Context().Value(pairedDeviceContextKey{}).(string)
	return ok
}

// Item 2kq (c): THE DISPATCHER.
//
// Every route docs/app-message-v1.md names is served by calling THE SAME HANDLER the
// local page calls. There is no second implementation of anything: a unit arrives over
// the broker session, becomes an in-process HTTP request against this server's own mux,
// and its response becomes a unit again. If the desktop's behaviour changes, the phone's
// changes with it, because it is the same code.
//
// THE PHONE'S IDENTITY, not the operator's browser. The request carries the broker-paired
// device's id in its context and no mutation token — the document is explicit that a
// device never learns one.
// Authority on this transport is the pairing, and revoking it ends every request.
//
// A CONTROL-PLANE PATH under item 2jy: this runs in the harness process, and no tool
// process is anywhere near it or the session it answers.

// appMessageRoutes is the closed set. A route name is not a URL path, so a device cannot
// compose a path this desktop never published; anything not in this table is answered
// 501 rather than guessed at.
var appMessageRoutes = map[string]struct {
	method string
	path   string
}{
	"message":           {http.MethodPost, "/api/message"},
	"stop":              {http.MethodPost, "/api/stop"},
	"approve":           {http.MethodPost, "/api/approve"},
	"tool":              {http.MethodPost, "/api/tools/"},
	"state":             {http.MethodGet, "/api/state"},
	"resync":            {http.MethodGet, "/api/state"},
	"chat.create":       {http.MethodPost, "/api/sessions"},
	"chat.rename":       {http.MethodPost, "/api/sessions/"},
	"chat.delete":       {http.MethodDelete, "/api/sessions/"},
	"chat.history":      {http.MethodGet, "/api/sessions/"},
	"chat.mirror":       {http.MethodPost, ""},
	"chat.mirror.since": {http.MethodPost, ""},
	"chat.mirror.take":  {http.MethodPost, ""},
}

type appRequestUnit struct {
	V     int             `json:"v"`
	Kind  string          `json:"kind"`
	ID    string          `json:"id"`
	Route string          `json:"route"`
	Body  json.RawMessage `json:"body"`
}

type appResponseUnit struct {
	V      int             `json:"v"`
	Kind   string          `json:"kind"`
	ID     string          `json:"id"`
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// DispatchAppMessage turns one decrypted request unit into one response unit. It never
// returns an error: a refusal is a response with a status, because the phone is owed an
// answer for every id it sent.
func (s *Server) DispatchAppMessage(deviceID string, unit []byte) []byte {
	var request appRequestUnit
	if err := json.Unmarshal(unit, &request); err != nil {
		return s.appProblem("", http.StatusBadRequest, "the unit is not valid JSON")
	}
	if request.V != 1 || request.Kind != "request" || request.ID == "" {
		return s.appProblem(request.ID, http.StatusBadRequest, "the unit is not an app-message v1 request")
	}
	if strings.HasPrefix(request.Route, "chat.mirror") {
		if _, known := appMessageRoutes[request.Route]; !known {
			return s.appProblem(request.ID, http.StatusNotImplemented, "this desktop does not publish the route "+request.Route)
		}
		return s.dispatchMirror(request)
	}
	route, known := appMessageRoutes[request.Route]
	if !known {
		// The document's own rule: a route this desktop did not publish is 501, which
		// is what makes adding one compatible.
		return s.appProblem(request.ID, http.StatusNotImplemented, "this desktop does not publish the route "+request.Route)
	}

	body := request.Body
	path := route.path
	switch request.Route {
	case "tool":
		// The path segment arrives as a field for the same reason the route set is
		// closed: a device never composes a path.
		var named struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &named); err != nil || strings.TrimSpace(named.Name) == "" {
			return s.appProblem(request.ID, http.StatusBadRequest, "the tool route needs a name")
		}
		if strings.ContainsAny(named.Name, "/?#%") {
			return s.appProblem(request.ID, http.StatusBadRequest, "a tool name is a name, not a path")
		}
		path += named.Name
	case "chat.create":
		// Item 2ow: a phone may choose one advertised connection for a new chat. It
		// still cannot choose a role, copy another chat, or send any local setting.
		if len(body) > 0 && string(body) != "null" {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				return s.appProblem(request.ID, http.StatusBadRequest, "the chat.create body is not an object")
			}
			for name := range fields {
				if name != "label" && name != "connection_id" {
					return s.appProblem(request.ID, http.StatusBadRequest, "chat.create carries label and connection_id only; it does not carry "+name)
				}
			}
		}
	case "chat.rename", "chat.delete":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			return s.appProblem(request.ID, http.StatusBadRequest, "the "+request.Route+" body is not an object")
		}
		var sessionID string
		if raw := fields["session_id"]; json.Unmarshal(raw, &sessionID) != nil || strings.TrimSpace(sessionID) == "" || strings.ContainsAny(sessionID, "/\\?#%") {
			return s.appProblem(request.ID, http.StatusBadRequest, request.Route+" needs a session_id")
		}
		path += url.PathEscape(sessionID)
		if request.Route == "chat.rename" {
			var label string
			if raw := fields["label"]; json.Unmarshal(raw, &label) != nil || strings.TrimSpace(label) == "" {
				return s.appProblem(request.ID, http.StatusBadRequest, "chat.rename needs a label")
			}
			for name := range fields {
				if name != "session_id" && name != "label" {
					return s.appProblem(request.ID, http.StatusBadRequest, "chat.rename carries session_id and label only; it does not carry "+name)
				}
			}
			body, _ = json.Marshal(map[string]string{"label": label})
		} else {
			for name := range fields {
				if name != "session_id" {
					return s.appProblem(request.ID, http.StatusBadRequest, "chat.delete carries session_id only; it does not carry "+name)
				}
			}
			body = nil
		}
	case "chat.history":
		var history struct {
			SessionID string `json:"session_id"`
			Before    *int   `json:"before"`
		}
		if err := json.Unmarshal(body, &history); err != nil || strings.TrimSpace(history.SessionID) == "" || strings.ContainsAny(history.SessionID, "/\\?#%") {
			return s.appProblem(request.ID, http.StatusBadRequest, "chat.history needs a session_id")
		}
		path += url.PathEscape(history.SessionID) + "/history"
		if history.Before != nil {
			if *history.Before < 0 {
				return s.appProblem(request.ID, http.StatusBadRequest, "chat.history before must be non-negative")
			}
			path += fmt.Sprintf("?before=%d", *history.Before)
		}
		body = nil
	case "resync":
		// One session's state. The route stands for the same GET the page makes.
		var session struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(body, &session); err == nil && session.SessionID != "" {
			path += "?session=" + session.SessionID
		}
		body = nil
	case "state":
		body = nil
	}

	request.Body = body
	return s.serveAsPhone(deviceID, request, route.method, path, body)
}

func (s *Server) serveAsPhone(deviceID string, request appRequestUnit, method, path string, body json.RawMessage) []byte {
	reader := strings.NewReader("")
	if len(body) > 0 && method != http.MethodGet {
		reader = strings.NewReader(string(body))
	}
	httpRequest := httptest.NewRequest(method, path, reader)
	httpRequest.Header.Set("Content-Type", "application/json")
	// The broker-paired phone's identity. No mutation token: a device never learns one.
	httpRequest = httpRequest.WithContext(context.WithValue(httpRequest.Context(), pairedDeviceContextKey{}, deviceID))
	recorder := httptest.NewRecorder()
	s.appMessageHandler().ServeHTTP(recorder, httpRequest)

	response := appResponseUnit{V: 1, Kind: "response", ID: request.ID, Status: recorder.Code}
	if payload := recorder.Body.Bytes(); len(payload) > 0 && json.Valid(payload) {
		response.Body = payload
	}
	out, err := json.Marshal(response)
	if err != nil {
		return s.appProblem(request.ID, http.StatusInternalServerError, "the response could not be encoded")
	}
	return out
}

// appMessageHandler is the mux WITHOUT the browser and mutation guards: those two are
// about a browser page's CSRF and session cookie, and this transport has neither. The
// authority here is the pairing, which the caller has already established and which
// revocation ends.
func (s *Server) appMessageHandler() http.Handler {
	return s.routes()
}

func (s *Server) appProblem(id string, status int, detail string) []byte {
	body, _ := json.Marshal(map[string]string{"error": detail})
	out, err := json.Marshal(appResponseUnit{V: 1, Kind: "response", ID: id, Status: status, Body: body})
	if err != nil {
		return []byte(fmt.Sprintf(`{"v":1,"kind":"response","id":%q,"status":500}`, id))
	}
	return out
}
