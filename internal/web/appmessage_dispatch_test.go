package web

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// state goes through GET /api/state and comes back as the same snapshot the page reads.
func TestTheStateRouteIsTheSameSnapshotThePageReads2kq(t *testing.T) {
	server := dispatchServer(t)
	response := dispatch(t, server, `{"v":1,"kind":"request","id":"aa","route":"state","body":{}}`)
	if response.Status != 200 {
		t.Fatalf("state = %d %s", response.Status, response.Body)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(response.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"sessions", "config", "connections"} {
		if _, ok := snapshot[field]; !ok {
			t.Errorf("the snapshot has no %s", field)
		}
	}
	// And the response carries the request's own id, because one response per id is the
	// whole of the return path.
	if response.ID != "aa" {
		t.Fatalf("the response id is %q", response.ID)
	}
}

// chat.create carries a label and NOTHING else.
func TestChatCreateCarriesALabelAndNothingElse2kq(t *testing.T) {
	server := dispatchServer(t)
	created := dispatch(t, server, `{"v":1,"kind":"request","id":"bb","route":"chat.create","body":{"label":"from the phone"}}`)
	if created.Status != 200 && created.Status != 201 {
		t.Fatalf("chat.create = %d %s", created.Status, created.Body)
	}
	if !strings.Contains(string(created.Body), "from the phone") {
		t.Errorf("the created chat does not carry the label: %s", created.Body)
	}
	// Anything else in the body is refused, so a phone cannot choose its model, its
	// role, or a chat to copy.
	for _, body := range []string{
		`{"label":"x","connection_id":"local"}`,
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
