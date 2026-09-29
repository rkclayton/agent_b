package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
)

// Item 2kq (c): THE DISPATCHER.
//
// Every route docs/app-message-v1.md names is served by calling THE SAME HANDLER the
// local page calls. There is no second implementation of anything: a unit arrives over
// the broker session, becomes an in-process HTTP request against this server's own mux,
// and its response becomes a unit again. If the desktop's behaviour changes, the phone's
// changes with it, because it is the same code.
//
// THE PHONE'S IDENTITY, not the operator's browser. The request carries the paired
// device's id in its context exactly as an enrolled phone's HTTP request does, and it
// carries no mutation token — the document is explicit that a device never learns one.
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
	"message":     {http.MethodPost, "/api/message"},
	"stop":        {http.MethodPost, "/api/stop"},
	"approve":     {http.MethodPost, "/api/approve"},
	"tool":        {http.MethodPost, "/api/tools/"},
	"state":       {http.MethodGet, "/api/state"},
	"resync":      {http.MethodGet, "/api/state"},
	"chat.create": {http.MethodPost, "/api/sessions"},
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
		// (c) and item 2my: a label and NOTHING ELSE. A body with any other field is
		// refused, so a phone cannot choose a connection, a role, or a chat to copy.
		if len(body) > 0 && string(body) != "null" {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				return s.appProblem(request.ID, http.StatusBadRequest, "the chat.create body is not an object")
			}
			for name := range fields {
				if name != "label" {
					return s.appProblem(request.ID, http.StatusBadRequest, "chat.create carries a label and nothing else; it does not carry "+name)
				}
			}
		}
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
	// The phone's identity, exactly as the phone session guard sets it for an enrolled
	// device's HTTP request. No mutation token: a device never learns one.
	httpRequest = httpRequest.WithContext(context.WithValue(httpRequest.Context(), phoneDeviceContextKey{}, deviceID))
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
