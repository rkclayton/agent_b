package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"harness/internal/broker"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

// Item 2kq (c): the dispatcher serves the SAME handlers the local page calls, as the
// paired phone's identity, and the route set is closed.

func dispatchServer(t *testing.T) *Server {
	t.Helper()
	root, err := os.MkdirTemp("", "agentb-dispatch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "harness.json")
	cfg := config.Defaults(root)
	cfg.Connections[0] = runnableTestConnection("local")
	remote := runnableTestConnection("remote")
	remote.Label, remote.Model, remote.BaseURL, remote.APIKey = "Remote GPU", "model-r", "https://models.example.test:8443/private/v1", "PLANTED-SECRET"
	remote.Capabilities.Vision, remote.Capabilities.DocumentInput, remote.Capabilities.ToolCalls, remote.Capabilities.NCtx = config.VisionReadsImages, true, true, 65536
	cfg.Connections = append(cfg.Connections, remote)
	cfg.Agents = []config.Agent{{Name: "local", B: "local", Toolset: config.FullToolset()}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	server := New(&cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	server.SetRegistry(session.NewRegistry(events.NewBus(), writers, server.Connection, cfg.Run.MaxTurns, server.ConfigSnapshot))
	return server
}

func dispatch(t *testing.T, server *Server, unit string) appResponseUnit {
	t.Helper()
	raw := server.DispatchAppMessage("device-1", []byte(unit))
	var response appResponseUnit
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("the dispatcher answered with something that is not a unit: %s", raw)
	}
	if response.V != 1 || response.Kind != "response" {
		t.Fatalf("the answer is not an app-message v1 response: %s", raw)
	}
	return response
}

func TestPhoneStateCarriesOnlyTheSafeConnectionSheet2ow(t *testing.T) {
	server := dispatchServer(t)
	response := dispatch(t, server, `{"v":1,"kind":"request","id":"aa","route":"state","body":{}}`)
	if response.Status != 200 {
		t.Fatalf("state = %d %s", response.Status, response.Body)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(response.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"sessions", "connections"} {
		if _, ok := snapshot[field]; !ok {
			t.Errorf("the snapshot has no %s", field)
		}
	}
	var connections []map[string]any
	if err := json.Unmarshal(snapshot["connections"], &connections); err != nil || len(connections) != 2 {
		t.Fatalf("connections=%v err=%v", connections, err)
	}
	remote := connections[1]
	if len(remote) != 8 {
		t.Errorf("connection carries fields outside the safe sheet: %v", remote)
	}
	for _, field := range []string{"id", "label", "model", "host", "vision", "docs", "tools", "ctx"} {
		if _, ok := remote[field]; !ok {
			t.Errorf("connection has no %s: %v", field, remote)
		}
	}
	if remote["host"] != "models.example.test:8443" || remote["ctx"] != float64(32768) {
		t.Errorf("safe remote=%v", remote)
	}
	rawConnections := string(snapshot["connections"])
	for _, secret := range []string{"PLANTED-SECRET", "/private/v1", "api_key", "credential", "base_url"} {
		if strings.Contains(rawConnections, secret) {
			t.Errorf("phone connection sheet leaked %q", secret)
		}
	}
	for _, state := range []string{"not paired", "broker unreachable", "holding", "phone connected"} {
		server.SetBrokerHost(statusBrokerHost{state: state})
		response = dispatch(t, server, `{"v":1,"kind":"request","id":"aa","route":"state","body":{}}`)
		var body struct {
			Broker broker.Status `json:"broker"`
		}
		if err := json.Unmarshal(response.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Broker.State != state {
			t.Fatalf("phone state=%q, local state=%q", body.Broker.State, state)
		}
	}
	// And the response carries the request's own id, because one response per id is the
	// whole of the return path.
	if response.ID != "aa" {
		t.Fatalf("the response id is %q", response.ID)
	}
}

func TestChatCreateAcceptsOneConnectionAndRefusesUnknownWithoutCreating2ow(t *testing.T) {
	server := dispatchServer(t)
	created := dispatch(t, server, `{"v":1,"kind":"request","id":"bb","route":"chat.create","body":{"label":"from the phone"}}`)
	if created.Status != 200 && created.Status != 201 {
		t.Fatalf("chat.create = %d %s", created.Status, created.Body)
	}
	if !strings.Contains(string(created.Body), "from the phone") {
		t.Errorf("the created chat does not carry the label: %s", created.Body)
	}
	selected := dispatch(t, server, `{"v":1,"kind":"request","id":"bc","route":"chat.create","body":{"label":"on remote","connection_id":"remote"}}`)
	if selected.Status != 201 || !strings.Contains(string(selected.Body), `"connection_id":"remote"`) {
		t.Fatalf("selected=%d %s", selected.Status, selected.Body)
	}
	before := len(server.registry.List())
	unknown := dispatch(t, server, `{"v":1,"kind":"request","id":"bd","route":"chat.create","body":{"connection_id":"missing"}}`)
	if unknown.Status != 400 || !strings.Contains(string(unknown.Body), `"field":"connection_id"`) || len(server.registry.List()) != before {
		t.Fatalf("unknown=%d %s sessions=%d->%d", unknown.Status, unknown.Body, before, len(server.registry.List()))
	}
	for _, body := range []string{
		`{"role":"d"}`,
		`{"source_session_id":"main"}`,
	} {
		refused := dispatch(t, server, `{"v":1,"kind":"request","id":"cc","route":"chat.create","body":`+body+`}`)
		if refused.Status != 400 {
			t.Errorf("chat.create with %s = %d, want 400", body, refused.Status)
		}
	}
}

// A route this desktop does not publish is 501, and a device cannot compose a path.
func TestAnUnpublishedRouteIsNotImplementedAndAPathCannotBeComposed2kq(t *testing.T) {
	server := dispatchServer(t)
	for _, route := range []string{"sessions.delete", "config", "../api/config", "tools"} {
		response := dispatch(t, server, `{"v":1,"kind":"request","id":"dd","route":"`+route+`","body":{}}`)
		if response.Status != 501 {
			t.Errorf("route %q = %d, want 501", route, response.Status)
		}
	}
	// The tool route carries a NAME, and a name that is a path is refused.
	for _, name := range []string{"../config", "shell/../../api/config", "a?b"} {
		response := dispatch(t, server, `{"v":1,"kind":"request","id":"ee","route":"tool","body":{"name":"`+name+`","session_id":"main","enabled":true}}`)
		if response.Status != 400 {
			t.Errorf("tool name %q = %d, want 400", name, response.Status)
		}
	}
}

// A malformed unit is answered rather than dropped: the phone is owed an answer for
// every id it sent.
func TestAMalformedUnitIsAnswered2kq(t *testing.T) {
	server := dispatchServer(t)
	for _, unit := range []string{
		`not json`,
		`{"v":2,"kind":"request","id":"ff","route":"state","body":{}}`,
		`{"v":1,"kind":"response","id":"ff","route":"state","body":{}}`,
		`{"v":1,"kind":"request","route":"state","body":{}}`,
	} {
		raw := server.DispatchAppMessage("device-1", []byte(unit))
		var response appResponseUnit
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatalf("no answer for %q: %s", unit, raw)
		}
		if response.Status != 400 {
			t.Errorf("%q = %d, want 400", unit, response.Status)
		}
	}
}

// The transport carries no mutation token, and the dispatcher does not need one: the
// authority is the pairing. This is the assertion that the guard the browser needs is
// not silently being satisfied by something the phone sent.
func TestTheDispatcherCarriesNoMutationToken2kq(t *testing.T) {
	server := dispatchServer(t)
	source, err := os.ReadFile(filepath.Join("appmessage_dispatch.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "mutation_token") || strings.Contains(string(source), "X-AgentB-Mutation-Token") {
		t.Fatal("the dispatcher carries a mutation token, which a device never learns")
	}
	// And a mutating route still works, which is the point: the pairing is the
	// authority, not a browser secret.
	response := dispatch(t, server, `{"v":1,"kind":"request","id":"gg","route":"chat.create","body":{"label":"paired"}}`)
	if response.Status != 200 && response.Status != 201 {
		t.Fatalf("a mutating route as the phone = %d %s", response.Status, response.Body)
	}
}

type appVector struct {
	Name    string          `json:"name"`
	Decoded json.RawMessage `json:"decoded"`
	Bytes   string          `json:"bytes"`
}

func loadAppVectors(t *testing.T) (file struct {
	Vectors []appVector `json:"vectors"`
	Split   struct {
		UnitMax    int         `json:"unit_max"`
		ChunkBytes int         `json:"chunk_bytes"`
		Unit       appVector   `json:"unit"`
		Parts      []appVector `json:"parts"`
	} `json:"split"`
}) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "appmessage", "testdata", "vectors-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

// Item 2o7: the encoder that sends these units reproduces every committed vector
// byte for byte, splits the oversize one exactly as the vector does, and
// reassembles it by its digest; a split against any budget fits that budget.
func TestTheEncoderReproducesEveryVector2o7(t *testing.T) {
	file := loadAppVectors(t)
	for _, entry := range append(append([]appVector{}, file.Vectors...), file.Split.Unit) {
		var decoded any
		decoder := json.NewDecoder(bytes.NewReader(entry.Decoded))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		if encoded, err := appCanonical(decoded); err != nil || string(encoded) != entry.Bytes {
			t.Errorf("%s: encoder gave %s (%v)", entry.Name, encoded, err)
		}
	}
	var first struct {
		UnitID string `json:"unit_id"`
	}
	_ = json.Unmarshal(file.Split.Parts[0].Decoded, &first)
	parts, err := appSplitWith([]byte(file.Split.Unit.Bytes), first.UnitID, file.Split.ChunkBytes)
	if err != nil || len(parts) != len(file.Split.Parts) {
		t.Fatalf("split gave %d parts (%v), the vector has %d", len(parts), err, len(file.Split.Parts))
	}
	for index, part := range parts {
		if string(part) != file.Split.Parts[index].Bytes {
			t.Errorf("part %d differs from the vector", index)
		}
	}
	if whole, err := appReassemble(parts); err != nil || string(whole) != file.Split.Unit.Bytes {
		t.Fatalf("reassembly: %v", err)
	}
	if _, err := appReassemble(parts[1:]); err == nil {
		t.Fatal("a missing part was not treated as a gap")
	}
	fresh, err := appSplit([]byte(file.Split.Unit.Bytes), file.Split.UnitMax)
	if err != nil || len(fresh) < 2 {
		t.Fatalf("split against the budget: %d parts, %v", len(fresh), err)
	}
	for index, part := range fresh {
		if len(part) > file.Split.UnitMax {
			t.Errorf("part %d is %d bytes, over %d", index, len(part), file.Split.UnitMax)
		}
	}
}

// recordingDevice is the broker client's downstream half, observed.
type recordingDevice struct {
	mu     sync.Mutex
	units  []map[string]any
	pushes []string
}

func (d *recordingDevice) Deliver(plaintext []byte) error {
	var unit map[string]any
	if err := json.Unmarshal(plaintext, &unit); err != nil {
		return err
	}
	d.mu.Lock()
	d.units = append(d.units, unit)
	d.mu.Unlock()
	return nil
}

func (d *recordingDevice) Notify(kind, chatID, notice string) error {
	d.mu.Lock()
	d.pushes = append(d.pushes, kind+" "+chatID)
	d.mu.Unlock()
	return nil
}

func (d *recordingDevice) waitFor(t *testing.T, what string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		d.mu.Lock()
		for _, unit := range d.units {
			if match(unit) {
				d.mu.Unlock()
				return unit
			}
		}
		d.mu.Unlock()
	}
	d.mu.Lock()
	kinds := []string{}
	for _, unit := range d.units {
		data, _ := unit["data"].(map[string]any)
		kinds = append(kinds, fmt.Sprintf("%v/%v/%v", unit["kind"], unit["session_id"], data["session_id"]))
	}
	d.mu.Unlock()
	t.Fatalf("the device never received %s; it received %v", what, kinds)
	return nil
}

// Item 2o7 (b)-(e) through the real server: a device connected to the stream gets a
// snapshot of every chat on connect; chat.create through the dispatcher makes a chat
// named as the desktop names one and the device sees it arrive as patches; a global
// event arrives as an event unit.
func TestAPairedDeviceSeesTheChatsAndIsAnswered2o7(t *testing.T) {
	server, registry, writers, _, _, root := consoleServer(t)
	defer writers.Close()
	// As main.go wires it: durable records feed the projection the stream reads.
	server.bus.SetSink(nil)
	server.bus.SetDurableSink(writers.WriteRecord, server.projector.Apply, server.projector.MarkStale)
	existing, err := registry.Create("existing chat", server.ConfigSnapshot().DefaultAgentID(), root)
	if err != nil {
		t.Fatal(err)
	}
	device := &recordingDevice{}
	ctx, stop := context.WithCancel(context.Background())
	streamed := make(chan struct{})
	defer func() { stop(); <-streamed }()
	go func() { server.streamToDevice(ctx, device); close(streamed) }()
	device.waitFor(t, "a snapshot of the existing chat", func(unit map[string]any) bool {
		return unit["kind"] == "snapshot" && unit["session_id"] == existing.ID && unit["v"] == float64(1)
	})

	answer := server.DispatchAppMessage("broker:test", []byte(`{"v":1,"kind":"request","id":"00112233445566778899aabbccddeeff","route":"chat.create","body":{}}`))
	var response struct {
		Kind   string `json:"kind"`
		ID     string `json:"id"`
		Status int    `json:"status"`
		Body   struct {
			Session struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"session"`
		} `json:"body"`
	}
	if err := json.Unmarshal(answer, &response); err != nil || response.Kind != "response" || response.ID != "00112233445566778899aabbccddeeff" || response.Status != http.StatusCreated || response.Body.Session.ID == "" {
		t.Fatalf("chat.create answered %s (%v)", answer, err)
	}
	device.waitFor(t, "a patch for the new chat", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		return unit["kind"] == "patch" && data["session_id"] == response.Body.Session.ID
	})
	for _, route := range []string{"config", "update", "plan.go", "../api/config"} {
		refused := server.DispatchAppMessage("broker:test", []byte(`{"v":1,"kind":"request","id":"ffeeddccbbaa99887766554433221100","route":"`+route+`","body":{}}`))
		if !strings.Contains(string(refused), `"status":501`) {
			t.Fatalf("route %q was not refused 501: %s", route, refused)
		}
	}

	server.bus.Publish(events.New(events.ApprovalRequired, existing.ID, "r1", map[string]any{"call_id": "c1"}))
	server.bus.Publish(events.New(events.RunStopped, existing.ID, "r1", map[string]any{"reason": "done"}))
	server.bus.Publish(events.New(events.ItemStuck, existing.ID, "r1", map[string]any{"item": "2ok"}))
	server.bus.Publish(events.New("broker.test.global", "", "", map[string]any{"note": "global"}))
	device.waitFor(t, "the global event", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		return unit["kind"] == "event" && data["type"] == "broker.test.global"
	})
	device.mu.Lock()
	pushes := append([]string(nil), device.pushes...)
	device.mu.Unlock()
	want := []string{"approval_required " + existing.ID, "run_stopped " + existing.ID, "item_stuck " + existing.ID}
	if strings.Join(pushes, "|") != strings.Join(want, "|") {
		t.Fatalf("pushes = %v, want %v", pushes, want)
	}
}

// Item 2o7 (a): a pairing starts the session and revoke ends it; with no pairing
// nothing is started.
func TestThePairedSessionStartsWithThePairingAndEndsWithRevoke2o7(t *testing.T) {
	server, _, writers, _, _, _ := consoleServer(t)
	defer writers.Close()
	dialed := make(chan struct{}, 8)
	client := &BrokerClient{status: broker.Status{State: "not paired"}, dial: func(context.Context) (broker.Transport, error) {
		dialed <- struct{}{}
		return nil, errors.New("scripted: no network in this test")
	}}
	server.SetBrokerHost(client)
	select {
	case <-dialed:
		t.Fatal("a connection was attempted with no pairing")
	case <-time.After(100 * time.Millisecond):
	}
	client.startSession(broker.Pairing{DeviceKeyID: []byte{1, 2, 3}})
	select {
	case <-dialed:
	case <-time.After(5 * time.Second):
		t.Fatal("the paired session never dialed")
	}
	client.mu.Lock()
	running := client.client != nil
	client.stopSessionLocked()
	stopped := client.client == nil
	client.mu.Unlock()
	if !running || !stopped {
		t.Fatalf("running=%v stopped=%v", running, stopped)
	}
	for len(dialed) > 0 {
		<-dialed
	}
	select {
	case <-dialed:
		t.Fatal("the session dialed again after it was stopped")
	case <-time.After(1500 * time.Millisecond):
	}
}
