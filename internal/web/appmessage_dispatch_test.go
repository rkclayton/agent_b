package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"harness/internal/broker"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/chatstore"
	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/projection"
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
	cfg.Agents = []config.Agent{{Name: "agent_b", B: "local", Toolset: config.FullToolset()}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, &cfg, path, root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	server.SetRegistry(session.NewRegistry(events.NewBus(), writers, server.Connection, 0, server.ConfigSnapshot))
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
		// 2wx: the broker's status is the PC's; neither phone reads it.
		var body map[string]json.RawMessage
		if err := json.Unmarshal(response.Body, &body); err != nil {
			t.Fatal(err)
		}
		if _, sent := body["broker"]; sent {
			t.Fatalf("phone state carries the broker status while it is %q", state)
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
	if selected.Status != 201 || !strings.Contains(string(selected.Body), `"agent_id":"agent_b"`) || !strings.Contains(string(selected.Body), `"connection_id":"local"`) {
		t.Fatalf("selected=%d %s", selected.Status, selected.Body)
	}
	agentID := server.ConfigSnapshot().DefaultAgentID()
	byAgent := dispatch(t, server, `{"v":1,"kind":"request","id":"be","route":"chat.create","body":{"agent_id":"`+agentID+`"}}`)
	if byAgent.Status != 201 || !strings.Contains(string(byAgent.Body), `"agent_id":"`+agentID+`"`) {
		t.Fatalf("agent selected=%d %s", byAgent.Status, byAgent.Body)
	}
	state := dispatch(t, server, `{"v":1,"kind":"request","id":"bf","route":"state"}`)
	if state.Status != 200 || !strings.Contains(string(state.Body), `"agents"`) {
		t.Fatalf("state agents=%d %s", state.Status, state.Body)
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

func TestPhoneJoinAndFolderChangesCarryChatListMetadata2qk(t *testing.T) {
	server, registry, writers, _, _, _ := consoleServer(t)
	defer writers.Close()
	server.bus.SetSink(nil)
	server.bus.SetDurableSink(writers.WriteRecord, server.projector.Apply, server.projector.MarkStale)
	chat, err := registry.Create("folder phone chat", server.ConfigSnapshot().DefaultAgentID(), "")
	if err != nil {
		t.Fatal(err)
	}
	postTree := func(body string) {
		response := httptest.NewRecorder()
		server.chatTree(response, httptest.NewRequest(http.MethodPost, "/api/chats/tree", strings.NewReader(body)))
		if response.Code != http.StatusOK {
			t.Fatalf("tree %s = %d %s", body, response.Code, response.Body.String())
		}
	}
	postTree(`{"action":"add","name":"Work"}`)
	postTree(`{"action":"move","id":"` + chat.ID + `","folder":"Work"}`)
	if _, err := server.projector.Snapshot(writers.SessionCursors()); err != nil {
		t.Fatal(err)
	}
	state := httptest.NewRecorder()
	server.state(state, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var stateBody struct {
		Folders  []string                       `json:"folders"`
		Sessions map[string]projection.Snapshot `json:"sessions"`
	}
	if err := json.Unmarshal(state.Body.Bytes(), &stateBody); err != nil || len(stateBody.Folders) != 1 || stateBody.Folders[0] != "Work" || stateBody.Sessions[chat.ID].Folder != "Work" || stateBody.Sessions[chat.ID].LastActivity != "" {
		t.Fatalf("page state chat list=%+v (%v)", stateBody, err)
	}

	device := &recordingDevice{}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { stop(); <-done }()
	go func() { server.streamUnitsToDevice(ctx, device); close(done) }()
	joined := device.waitFor(t, "the folder and no invented activity in the joined chat", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		return unit["kind"] == "snapshot" && unit["session_id"] == chat.ID && data["folder"] == "Work" && data["last_activity"] == nil
	})
	if data := joined["data"].(map[string]any); data["workspace"] == "Work" {
		t.Fatal("folder was substituted for workspace")
	}
	device.waitFor(t, "the joined folder list", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		payload, _ := data["data"].(map[string]any)
		folders, _ := payload["folders"].([]any)
		return unit["kind"] == "event" && data["type"] == "chat.list.snapshot" && len(folders) == 1 && folders[0] == "Work"
	})

	renameStarted := time.Now()
	postTree(`{"action":"rename","path":"Work","name":"Projects"}`)
	device.waitFor(t, "the folder rename patch", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		payload, _ := data["data"].(map[string]any)
		return unit["kind"] == "event" && data["type"] == "chat.list.patch" && payload["operation"] == "rename" && payload["path"] == "Work" && payload["value"] == "Projects"
	})
	if elapsed := time.Since(renameStarted); elapsed >= 2*time.Second {
		t.Fatalf("folder rename patch took %s", elapsed)
	}
	postTree(`{"action":"add","name":"Later"}`)
	moveStarted := time.Now()
	postTree(`{"action":"move","id":"` + chat.ID + `","folder":"Later"}`)
	device.waitFor(t, "the chat move patch", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		payload, _ := data["data"].(map[string]any)
		return unit["kind"] == "event" && data["type"] == "chat.list.patch" && payload["operation"] == "move" && payload["session_id"] == chat.ID && payload["folder"] == "Later"
	})
	if elapsed := time.Since(moveStarted); elapsed >= 2*time.Second {
		t.Fatalf("chat move patch took %s", elapsed)
	}
	server.bus.Publish(events.New(events.MessageAppended, chat.ID, "", map[string]any{"message": map[string]any{"id": "newest", "role": "user", "content": "newest activity"}}))
	device.waitFor(t, "the newest-activity list patch", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		payload, _ := data["data"].(map[string]any)
		return unit["kind"] == "event" && data["type"] == "chat.list.patch" && payload["operation"] == "activity" && payload["session_id"] == chat.ID && payload["last_activity"] != ""
	})
}

func TestDesktopPinLeavesPhoneChatListBytesUnchanged2qz(t *testing.T) {
	server, registry, writers, _, _, _ := consoleServer(t)
	defer writers.Close()
	server.bus.SetSink(nil)
	server.bus.SetDurableSink(writers.WriteRecord, server.projector.Apply, server.projector.MarkStale)
	chat, err := registry.Create("phone-stable", server.ConfigSnapshot().DefaultAgentID(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.projector.Snapshot(writers.SessionCursors()); err != nil {
		t.Fatal(err)
	}
	phoneBytes := func() []byte {
		sessions := server.projector.CurrentSnapshot()
		server.decorateChatList(sessions)
		body, marshalErr := json.Marshal(server.firstScreenSessions(sessions, "-"))
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return body
	}
	before := phoneBytes()
	response := httptest.NewRecorder()
	server.chatTree(response, httptest.NewRequest(http.MethodPost, "/api/chats/tree", strings.NewReader(`{"action":"pin","id":"`+chat.ID+`","pinned":true}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("pin = %d %s", response.Code, response.Body.String())
	}
	if after := phoneBytes(); !bytes.Equal(before, after) {
		t.Fatalf("phone bytes changed after desktop pin\nbefore=%s\nafter=%s", before, after)
	}
}

func TestPhoneRenameAndDeleteUseTheDesktopHandlers2qk(t *testing.T) {
	server, _, writers, _, _, _ := consoleServer(t)
	defer writers.Close()
	server.bus.SetSink(nil)
	server.bus.SetDurableSink(writers.WriteRecord, server.projector.Apply, server.projector.MarkStale)
	created := dispatch(t, server, `{"v":1,"kind":"request","id":"new","route":"chat.create","body":{"label":"before"}}`)
	var body struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(created.Body, &body); err != nil || body.Session.ID == "" {
		t.Fatalf("create=%d %s (%v)", created.Status, created.Body, err)
	}
	device := &recordingDevice{}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { stop(); <-done }()
	go func() { server.streamUnitsToDevice(ctx, device); close(done) }()
	device.waitFor(t, "the chat before phone mutation", func(unit map[string]any) bool {
		return unit["kind"] == "snapshot" && unit["session_id"] == body.Session.ID
	})
	rename := dispatch(t, server, `{"v":1,"kind":"request","id":"rename","route":"chat.rename","body":{"session_id":"`+body.Session.ID+`","label":"after"}}`)
	item, found := server.registry.Get(body.Session.ID)
	if rename.Status != http.StatusOK || !found || item.Snapshot().Label != "after" {
		t.Fatalf("rename=%d %s found=%v", rename.Status, rename.Body, found)
	}
	device.waitFor(t, "the ordinary rename projection patch", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		return unit["kind"] == "patch" && data["session_id"] == body.Session.ID && strings.Contains(fmt.Sprint(data["operations"]), "/label")
	})
	deleted := dispatch(t, server, `{"v":1,"kind":"request","id":"delete","route":"chat.delete","body":{"session_id":"`+body.Session.ID+`"}}`)
	if deleted.Status != http.StatusOK {
		t.Fatalf("delete=%d %s", deleted.Status, deleted.Body)
	}
	if _, found := server.registry.Get(body.Session.ID); found {
		t.Fatal("phone delete left the chat in the desktop registry")
	}
	device.waitFor(t, "the ordinary chat.deleted event", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		return unit["kind"] == "event" && data["type"] == events.ChatDeleted && strings.Contains(fmt.Sprint(data["data"]), body.Session.ID)
	})
	for _, route := range []string{"chat.move", "chat.close", "chat.reopen", "chat.archive", "config"} {
		response := dispatch(t, server, `{"v":1,"kind":"request","id":"no","route":"`+route+`","body":{}}`)
		if response.Status != http.StatusNotImplemented {
			t.Errorf("route %q = %d, want 501", route, response.Status)
		}
	}
}

func TestChatOpenAnswersOnlyWhatThePhoneLacks2rf(t *testing.T) {
	server := measuredServer(t, 1, 100)
	before := server.projector.CurrentSnapshot()["chat-0"]
	for index := 0; index < 10; index++ {
		server.bus.Publish(events.New(events.SessionRenamed, "chat-0", "run", map[string]any{"label": fmt.Sprintf("tail-%d", index)}))
	}
	open := func(id string, body any) appResponseUnit {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return dispatch(t, server, `{"v":1,"kind":"request","id":"`+id+`","route":"chat.open","body":`+string(encoded)+`}`)
	}
	decode := func(answer appResponseUnit) map[string]json.RawMessage {
		if answer.Status != http.StatusOK {
			t.Fatalf("chat.open = %d %s", answer.Status, answer.Body)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(answer.Body, &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	if body := decode(open("open-page", map[string]any{"session_id": "chat-0"})); len(body["snapshot"]) == 0 || len(body["patches"]) != 0 {
		t.Fatalf("no-have answer = %s", open("open-page-again", map[string]any{"session_id": "chat-0"}).Body)
	}
	current := server.projector.CurrentSnapshot()["chat-0"]
	if body := decode(open("current", map[string]any{"session_id": "chat-0", "have": current.Cursor})); string(body["patches"]) != "[]" {
		t.Fatalf("current cursor answer = %s", body["patches"])
	}
	tail := decode(open("tail", map[string]any{"session_id": "chat-0", "have": before.Cursor}))
	var patches []projection.Patch
	if err := json.Unmarshal(tail["patches"], &patches); err != nil || len(patches) != 10 {
		t.Fatalf("ten-event tail = %d patches (%v): %s", len(patches), err, tail["patches"])
	}
	applied, expected, err := projection.ProjectTailPatches(server.writers.SessionCursors()["chat-0"].Path, before.Cursor.Offset, before)
	if err != nil || !reflect.DeepEqual(patches, expected) || !reflect.DeepEqual(applied, current) {
		t.Fatalf("phone-applied tail differs from the PC snapshot: %v", err)
	}
	if body := decode(open("wrong-generation", map[string]any{"session_id": "chat-0", "have": projection.Cursor{Generation: "other", Offset: before.Cursor.Offset}})); len(body["snapshot"]) == 0 {
		t.Fatalf("wrong generation answer = %v", body)
	}
	if body := decode(open("ahead", map[string]any{"session_id": "chat-0", "have": projection.Cursor{Generation: current.Cursor.Generation, Offset: current.Cursor.Offset + 1}})); len(body["snapshot"]) == 0 {
		t.Fatalf("ahead answer = %v", body)
	}
	for index := 0; index < 100; index++ {
		server.bus.Publish(events.New(events.MessageAppended, "chat-0", "run", map[string]any{"message": map[string]any{
			"id": fmt.Sprintf("far-%d", index), "role": "assistant", "content": strings.Repeat("far", 40),
		}}))
	}
	if body := decode(open("far", map[string]any{"session_id": "chat-0", "have": before.Cursor})); len(body["snapshot"]) == 0 {
		t.Fatalf("large tail did not choose the smaller snapshot: %v", body)
	}
	preRestart := server.projector.CurrentSnapshot()["chat-0"].Cursor
	restarted := projection.NewStore()
	if _, err := restarted.Snapshot(server.writers.SessionCursors()); err != nil {
		t.Fatal(err)
	}
	server.SetProjection(restarted, server.writers)
	if body := decode(open("restart", map[string]any{"session_id": "chat-0", "have": preRestart})); string(body["patches"]) != "[]" {
		t.Fatalf("pre-restart cursor answer = %s", body["patches"])
	}
}

func TestChatOpenMakesOnlyThatChatLive2rf(t *testing.T) {
	server := measuredServer(t, 2, 100)
	live, device := newPhoneStreamState(), &recordingDevice{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { server.streamUnitsToDeviceOpening(ctx, device, nil, live); close(done) }()
	defer func() { cancel(); <-done }()
	device.waitFor(t, "the second chat join", func(unit map[string]any) bool { return unit["kind"] == "snapshot" && unit["session_id"] == "chat-1" })
	answer := server.dispatchAppMessage("device-1", []byte(`{"v":1,"kind":"request","id":"open","route":"chat.open","body":{"session_id":"chat-1"}}`), live)
	if !live.preparedAnswer() {
		t.Fatalf("open answer = %s", answer)
	}
	live.activate()
	device.mu.Lock()
	device.units = nil
	device.mu.Unlock()
	server.bus.Publish(events.New(events.RunStarted, "chat-0", "busy", map[string]any{"run_id": "busy"}))
	for index := 0; index < 60; index++ {
		server.bus.Publish(events.New(events.MessageAppended, "chat-0", "busy", map[string]any{"message": map[string]any{
			"id": fmt.Sprintf("busy-%d", index), "role": "assistant", "content": "busy transcript must not cross",
		}}))
	}
	device.waitFor(t, "the busy chat row state", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		payload, _ := data["data"].(map[string]any)
		row, _ := payload["session"].(map[string]any)
		run, _ := row["run"].(map[string]any)
		return data["type"] == events.ChatListPatch && payload["operation"] == "upsert" && row["id"] == "chat-0" && run["status"] == "running"
	})
	time.Sleep(100 * time.Millisecond)
	device.mu.Lock()
	afterBytes := 0
	for _, unit := range device.units {
		encoded, _ := appCanonical(unit)
		afterBytes += len(encoded)
		data, _ := unit["data"].(map[string]any)
		if unit["kind"] == "patch" && data["session_id"] == "chat-0" {
			device.mu.Unlock()
			t.Fatal("the unopened busy chat sent transcript patches")
		}
	}
	device.mu.Unlock()
	t.Logf("2rf ten-minute stand-in after chat.open: %d bytes (before 999521)", afterBytes)
	server.bus.Publish(events.New(events.MessageAppended, "chat-1", "open", map[string]any{"message": map[string]any{"id": "open-1", "role": "assistant", "content": "open chat"}}))
	device.waitFor(t, "the opened chat patch", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		return unit["kind"] == "patch" && data["session_id"] == "chat-1"
	})
	server.dispatchAppMessage("device-1", []byte(`{"v":1,"kind":"request","id":"none","route":"chat.open","body":{"session_id":""}}`), live)
	live.activate()
	device.mu.Lock()
	device.units = nil
	device.mu.Unlock()
	server.bus.Publish(events.New(events.MessageAppended, "chat-1", "open", map[string]any{"message": map[string]any{"id": "none-1", "role": "assistant", "content": "none"}}))
	time.Sleep(100 * time.Millisecond)
	device.mu.Lock()
	defer device.mu.Unlock()
	for _, unit := range device.units {
		if unit["kind"] == "patch" {
			t.Fatal("chat.open with an empty session left a live chat")
		}
	}
}

func TestAChangeLandingDuringChatOpenIsSentOnce2rf(t *testing.T) {
	live := newPhoneStreamState()
	epoch := live.begin("chat-1")
	patch := projection.Patch{SessionID: "chat-1", PreviousCursor: projection.Cursor{Generation: "one", Offset: 1}, Cursor: projection.Cursor{Generation: "one", Offset: 2}}
	result := make(chan bool, 1)
	go func() { result <- live.accept(patch) }()
	live.prepare(epoch, patch.PreviousCursor)
	live.activate()
	if !<-result || live.accept(patch) {
		t.Fatal("the patch concurrent with chat.open was missed or accepted twice")
	}
	if !newPhoneStreamState().accept(patch) {
		t.Fatal("a released phone no longer receives the legacy patch stream")
	}
}

func TestRunningChatAnswerPrecedesThreeHundredPatches2vc(t *testing.T) {
	for _, route := range []string{"resync", "chat.open"} {
		t.Run(route, func(t *testing.T) {
			server := measuredServer(t, 1, 1)
			live, device := newPhoneStreamState(), &recordingDevice{}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { server.streamUnitsToDeviceOpening(ctx, device, nil, live); close(done) }()
			defer func() { cancel(); <-done }()
			device.waitFor(t, "the initial snapshot", func(unit map[string]any) bool { return unit["kind"] == "snapshot" && unit["session_id"] == "chat-0" })
			device.mu.Lock()
			device.units = nil
			device.mu.Unlock()

			request := fmt.Sprintf(`{"v":1,"kind":"request","id":"open","route":%q,"body":{"session_id":"chat-0"}}`, route)
			answer := server.dispatchAppMessage("device-1", []byte(request), live)
			for index := 0; index < 300; index++ {
				server.bus.Publish(events.New(events.SessionRenamed, "chat-0", "run", map[string]any{"label": fmt.Sprintf("live-%03d", index)}))
				time.Sleep(2 * time.Millisecond)
			}
			if err := device.Deliver(answer); err != nil {
				t.Fatal(err)
			}
			live.activate()
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				device.mu.Lock()
				patches := 0
				for _, unit := range device.units {
					if unit["kind"] == "patch" {
						patches++
					}
				}
				device.mu.Unlock()
				if patches == 300 {
					break
				}
			}
			device.mu.Lock()
			defer device.mu.Unlock()
			seenAnswer, patches := false, 0
			var expectedCursor map[string]any
			for _, unit := range device.units {
				if unit["kind"] == "response" {
					seenAnswer = true
					body, _ := unit["body"].(map[string]any)
					if route == "resync" {
						sessions, _ := body["sessions"].(map[string]any)
						chat, _ := sessions["chat-0"].(map[string]any)
						expectedCursor, _ = chat["cursor"].(map[string]any)
					} else {
						snapshot, _ := body["snapshot"].(map[string]any)
						expectedCursor, _ = snapshot["cursor"].(map[string]any)
					}
				}
				if unit["kind"] == "patch" {
					if !seenAnswer {
						t.Fatal("a running-chat patch arrived before its answer")
					}
					data, _ := unit["data"].(map[string]any)
					if previous, _ := data["previous_cursor"].(map[string]any); !reflect.DeepEqual(previous, expectedCursor) {
						t.Fatalf("patch %d has a cursor gap: previous=%v want=%v", patches+1, previous, expectedCursor)
					}
					expectedCursor, _ = data["cursor"].(map[string]any)
					patches++
				}
			}
			if patches != 300 {
				t.Fatalf("after the answer: %d patches, want 300", patches)
			}
		})
	}
}

func TestIdleChatOpenBytesAndDeliveryFailure2vc(t *testing.T) {
	server := measuredServer(t, 1, 1)
	request := []byte(`{"v":1,"kind":"request","id":"idle","route":"chat.open","body":{"session_id":"chat-0"}}`)
	want := server.dispatchAppMessage("device-1", request, nil)
	for index := 0; index < 5; index++ {
		live := newPhoneStreamState()
		got := server.dispatchAppMessage("device-1", request, live)
		if !bytes.Equal(got, want) {
			t.Fatalf("idle open %d changed its response bytes", index+1)
		}
		failure := errors.New("transport closed while sending")
		returned := deliverPhoneAnswer(deviceSinkFunc(func([]byte) error { return failure }), server, "idle", got, live)
		var response appResponseUnit
		if json.Unmarshal(returned, &response) != nil || response.Status != http.StatusServiceUnavailable || !strings.Contains(string(response.Body), failure.Error()) {
			t.Fatalf("failed delivery answer = %s", returned)
		}
	}
}

type deviceSinkFunc func([]byte) error

func (fn deviceSinkFunc) Deliver(unit []byte) error { return fn(unit) }

func TestChatNameDoesNotWaitForItsStoredFolder2sn(t *testing.T) {
	server, _, writers, _, _, _ := consoleServer(t)
	defer writers.Close()
	created := dispatch(t, server, `{"v":1,"kind":"request","id":"new","route":"chat.create","body":{"label":"before"}}`)
	var body struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(created.Body, &body); err != nil || body.Session.ID == "" {
		t.Fatalf("create=%d %s (%v)", created.Status, created.Body, err)
	}
	item, found := server.registry.Get(body.Session.ID)
	if !found || !item.Snapshot().Scratch {
		t.Fatalf("created chat is not stored: found=%v", found)
	}
	if err := os.RemoveAll(item.Workspace); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+body.Session.ID, strings.NewReader(`{"label":"after"}`))
	request.Header.Set("Content-Type", "application/json")
	server.session(response, request)
	if response.Code != http.StatusOK || item.Snapshot().Label != "after" {
		t.Fatalf("rename=%d %s label=%q", response.Code, response.Body.String(), item.Snapshot().Label)
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
	mu    sync.Mutex
	units []map[string]any
	at    []time.Time
}

func (d *recordingDevice) Deliver(plaintext []byte) error {
	var unit map[string]any
	if err := json.Unmarshal(plaintext, &unit); err != nil {
		return err
	}
	d.mu.Lock()
	d.units, d.at = append(d.units, unit), append(d.at, time.Now())
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
	// A listed global event (2wt: only deviceSessionlessEvents reach a device).
	server.bus.Publish(events.New(events.ChatDeleted, "", "", map[string]any{"session_id": "gone"}))
	device.waitFor(t, "the global event", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		return unit["kind"] == "event" && data["type"] == events.ChatDeleted
	})
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

func TestPairingAgainStopsTheOldSessionBeforeTheNewDial2t1(t *testing.T) {
	server, _, writers, _, _, _ := consoleServer(t)
	defer writers.Close()
	var mu sync.Mutex
	var first context.Context
	secondSawStopped := make(chan bool, 1)
	dials := 0
	client := &BrokerClient{status: broker.Status{State: "not paired"}, dial: func(ctx context.Context) (broker.Transport, error) {
		mu.Lock()
		dials++
		call := dials
		if call == 1 {
			first = ctx
		}
		if call == 2 {
			secondSawStopped <- first != nil && first.Err() != nil
		}
		mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	server.SetBrokerHost(client)
	client.startSession(broker.Pairing{DeviceKeyID: []byte{1}})
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		started := first != nil
		mu.Unlock()
		if started || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	client.startSession(broker.Pairing{DeviceKeyID: []byte{2}})
	select {
	case stopped := <-secondSawStopped:
		if !stopped {
			t.Fatal("the replacement dial began before the old session stopped")
		}
	case <-time.After(time.Second):
		t.Fatal("the replacement session did not dial")
	}
	client.mu.Lock()
	client.stopSessionLocked()
	client.mu.Unlock()
}

// Item 2ws: each agents row a device reads carries that agent's tools, the
// read-only row says so, and a `tool` naming an agent reaches the device.
func TestADeviceSeesEachAgentsToolsAndAToggleReachesIt2ws(t *testing.T) {
	server := dispatchServer(t)
	server.mu.Lock()
	server.cfg.Agents = append(server.cfg.Agents, config.Agent{Name: "Second", B: "remote", Toolset: []string{"read_file", "shell"}})
	server.mu.Unlock()
	second := config.AgentID("Second")
	type row struct {
		ID       string `json:"id"`
		ReadOnly bool   `json:"read_only"`
		Tools    []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"tools"`
	}
	rows := func() map[string]row {
		response := dispatch(t, server, `{"v":1,"kind":"request","id":"aa","route":"state","body":{}}`)
		var body struct {
			Agents []row `json:"agents"`
		}
		if err := json.Unmarshal(response.Body, &body); err != nil || response.Status != 200 {
			t.Fatalf("state = %d %s (%v)", response.Status, response.Body, err)
		}
		byID := map[string]row{}
		for _, agent := range body.Agents {
			byID[agent.ID] = agent
		}
		return byID
	}
	enabled := func(agent row, name string) (bool, bool) {
		for _, tool := range agent.Tools {
			if tool.Name == name {
				return tool.Enabled, true
			}
		}
		return false, false
	}
	agents := rows()
	if len(agents["agent_b"].Tools) == 0 || !agents["agent_b"].ReadOnly {
		t.Fatalf("agent_b row = %+v", agents["agent_b"])
	}
	if on, ok := enabled(agents[second], "shell"); !ok || !on || agents[second].ReadOnly {
		t.Fatalf("second row = %+v", agents[second])
	}
	if on, ok := enabled(agents[second], "write_file"); !ok || on {
		t.Fatalf("second row lists write_file %v %v: %+v", on, ok, agents[second])
	}
	device := &recordingDevice{}
	ctx, stop := context.WithCancel(context.Background())
	streamed := make(chan struct{})
	defer func() { stop(); <-streamed }()
	go func() { server.streamToDevice(ctx, device); close(streamed) }()
	time.Sleep(50 * time.Millisecond)
	toggled := dispatch(t, server, `{"v":1,"kind":"request","id":"ab","route":"tool","body":{"name":"shell","agent_id":"`+second+`","enabled":false}}`)
	if toggled.Status != 200 {
		t.Fatalf("tool = %d %s", toggled.Status, toggled.Body)
	}
	if on, _ := enabled(rows()[second], "shell"); on {
		t.Fatal("the state row still has shell enabled after the toggle")
	}
	device.waitFor(t, "the agent's tools after the toggle", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		payload, _ := data["data"].(map[string]any)
		agent, _ := payload["agent"].(map[string]any)
		if unit["kind"] != "event" || data["type"] != events.AgentTools || agent["id"] != second {
			return false
		}
		tools, _ := agent["tools"].([]any)
		for _, tool := range tools {
			if entry, _ := tool.(map[string]any); entry["name"] == "shell" {
				return entry["enabled"] == false
			}
		}
		return false
	})
	refused := dispatch(t, server, `{"v":1,"kind":"request","id":"ac","route":"tool","body":{"name":"shell","agent_id":"agent_b","enabled":false}}`)
	if refused.Status != 400 || !strings.Contains(string(refused.Body), `"error":"agent_b tools are read-only"`) || !strings.Contains(string(refused.Body), `"field":"agent_id"`) {
		t.Fatalf("read-only refusal = %d %s", refused.Status, refused.Body)
	}
}

// Item 2wt (a): a device receives a session-less event only when its type is
// on deviceSessionlessEvents; anything else is dropped and counted.
func TestADeviceReceivesOnlyTheListedSessionlessEvents2wt(t *testing.T) {
	server := dispatchServer(t)
	device := &recordingDevice{}
	ctx, stop := context.WithCancel(context.Background())
	streamed := make(chan struct{})
	defer func() { stop(); <-streamed }()
	go func() { server.streamToDevice(ctx, device); close(streamed) }()
	time.Sleep(50 * time.Millisecond)
	server.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": map[string]any{"workspace": "C:/stand-in"}}))
	server.bus.Publish(events.New(events.UpdateChanged, "", "", map[string]any{"state": "idle"}))
	server.bus.Publish(events.New(events.ConnectionHealth, "", "", map[string]any{"id": "local"}))
	server.bus.Publish(events.New(events.ChatDeleted, "", "", map[string]any{"session_id": "gone"}))
	server.bus.Publish(events.New(events.ChatListPatch, "", "", map[string]any{"operation": "add", "path": "folder"}))
	device.waitFor(t, "the chat list patch", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		return data["type"] == events.ChatListPatch
	})
	device.mu.Lock()
	got := map[string]int{}
	for _, unit := range device.units {
		if data, _ := unit["data"].(map[string]any); unit["kind"] == "event" {
			got[fmt.Sprint(data["type"])]++
		}
	}
	device.mu.Unlock()
	if got[events.ChatDeleted] != 1 || got[events.ConfigChanged]+got[events.UpdateChanged]+got[events.ConnectionHealth] != 0 {
		t.Fatalf("the device received %v", got)
	}
	dropped := server.deviceEventsDropped()
	if dropped[events.ConfigChanged] != 1 || dropped[events.UpdateChanged] != 1 || dropped[events.ConnectionHealth] != 1 || len(dropped) != 3 {
		t.Fatalf("dropped = %v", dropped)
	}
	document, err := os.ReadFile(filepath.Join("..", "..", "docs", "app-message-v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	listed := "A device receives these session-less events and no others: `" + strings.Join(deviceSessionlessEvents, "`, `") + "`."
	if !strings.Contains(string(document), listed) {
		t.Fatalf("docs/app-message-v1.md does not say %q", listed)
	}
}

// Item 2wt (b): every row the PROFILES page lists from state.tools toggles.
func TestEveryListedToolTogglesOnAnAgent2wt(t *testing.T) {
	server := dispatchServer(t)
	server.mu.Lock()
	server.cfg.Agents = append(server.cfg.Agents, config.Agent{Name: "Second", B: "remote", Toolset: config.FullToolset()})
	server.mu.Unlock()
	second := config.AgentID("Second")
	listed := server.snapshotWithSessions(map[string]any{}, false)["tools"].([]map[string]string)
	refused := []string{}
	for _, tool := range listed {
		answer := dispatch(t, server, `{"v":1,"kind":"request","id":"ad","route":"tool","body":{"name":"`+tool["name"]+`","agent_id":"`+second+`","enabled":false}}`)
		if answer.Status != 200 {
			refused = append(refused, fmt.Sprintf("%s=%d", tool["name"], answer.Status))
		}
	}
	if len(refused) > 0 || len(listed) != len(config.FullToolset()) {
		t.Fatalf("listed %d of %d tools; refused %v", len(listed), len(config.FullToolset()), refused)
	}
}

// Item 2wu: a stand-in reply streamed to a paired device; mid runs after each
// token. Returns the device's patch units in arrival order, when the last text
// arrived, and when the PC had the last token: when its patch reached a projection
// subscriber, as the PC's own page gets it.
func streamStandInReplyToDevice(t *testing.T, tokens int, every time.Duration, mid func(index int, chat string, bus *events.Bus)) ([]map[string]any, time.Time, time.Time) {
	t.Helper()
	server, registry, writers, _, _, root := consoleServer(t)
	t.Cleanup(func() { writers.Close() })
	server.bus.SetSink(nil)
	server.bus.SetDurableSink(writers.WriteRecord, server.projector.Apply, server.projector.MarkStale)
	server.bus.SetTransientSink(server.projector.ApplyTransient)
	chat, err := registry.Create("streamed", server.ConfigSnapshot().DefaultAgentID(), root)
	if err != nil {
		t.Fatal(err)
	}
	device := &recordingDevice{}
	ctx, stop := context.WithCancel(context.Background())
	streamed := make(chan struct{})
	t.Cleanup(func() { stop(); <-streamed })
	go func() { server.streamToDevice(ctx, device); close(streamed) }()
	device.waitFor(t, "the chat's snapshot", func(unit map[string]any) bool { return unit["kind"] == "snapshot" && unit["session_id"] == chat.ID })
	_, pcPatches, unsubscribe, err := server.projector.SubscribeSnapshot(server.writers.SessionCursors())
	if err != nil {
		t.Fatal(err)
	}
	var pcMu sync.Mutex
	var ended time.Time
	pcDone := make(chan struct{})
	go func() {
		defer close(pcDone)
		for patch := range pcPatches {
			for _, operation := range patch.Operations {
				if operation.Op == "append" && strings.HasSuffix(operation.Path, "/text") {
					pcMu.Lock()
					ended = time.Now()
					pcMu.Unlock()
				}
			}
		}
	}()
	server.bus.Publish(events.New(events.RunStarted, chat.ID, "r1", map[string]any{}))
	server.bus.Publish(events.New(events.ModelRequest, chat.ID, "r1", map[string]any{"turn": 1}))
	var pc strings.Builder
	for index := 0; index < tokens; index++ {
		token := fmt.Sprintf("t%d ", index)
		pc.WriteString(token)
		server.bus.Publish(events.New(events.ModelDelta, chat.ID, "r1", map[string]any{"turn": 1, "kind": "content", "text": token}))
		if mid != nil {
			mid(index, chat.ID, server.bus)
		}
		time.Sleep(every)
	}
	device.waitFor(t, "the whole text", func(map[string]any) bool { return deviceText(device.units) == pc.String() })
	unsubscribe()
	<-pcDone
	server.bus.Publish(events.New(events.ModelResponse, chat.ID, "r1", map[string]any{"turn": 1, "content": pc.String()}))
	device.waitFor(t, "the response", func(unit map[string]any) bool {
		encoded, _ := json.Marshal(unit)
		return unit["kind"] == "patch" && strings.Contains(string(encoded), `"done":true`)
	})
	device.mu.Lock()
	defer device.mu.Unlock()
	units := []map[string]any{}
	var last time.Time
	for index, unit := range device.units {
		if data, _ := unit["data"].(map[string]any); unit["kind"] == "patch" && data["session_id"] == chat.ID {
			units = append(units, unit)
			if deviceText([]map[string]any{unit}) != "" {
				last = device.at[index]
			}
		}
	}
	return units, last, ended
}

// deviceText is the reply text a device rebuilds from the units' appends.
func deviceText(units []map[string]any) string {
	var text strings.Builder
	for _, unit := range units {
		data, _ := unit["data"].(map[string]any)
		operations, _ := data["operations"].([]any)
		for _, operation := range operations {
			if op, _ := operation.(map[string]any); op["op"] == "append" && strings.HasSuffix(fmt.Sprint(op["path"]), "/text") {
				text.WriteString(fmt.Sprint(op["value"]))
			}
		}
	}
	return text.String()
}

func deviceUnitBytes(units []map[string]any) int {
	total := 0
	for _, unit := range units {
		encoded, _ := json.Marshal(unit)
		total += len(encoded)
	}
	return total
}

// raceTickAllowance is the last token's bound under -race (item 2wz): the detector
// slows the scheduler several-fold, and hosted CI measured 162-206 ms where an
// unraced run measures 18-40 ms; the 2wu guarantee is measured without it.
const raceTickAllowance = 4 * deviceStreamTick

func TestAStreamedReplyReachesADeviceInTicks2wu(t *testing.T) {
	const tokens, every = 1000, 20 * time.Millisecond
	started := time.Now()
	units, last, ended := streamStandInReplyToDevice(t, tokens, every, nil)
	duration := time.Since(started)
	text, deltas := 0, 0
	for _, unit := range units {
		if deviceText([]map[string]any{unit}) != "" {
			text++
		}
		encoded, _ := json.Marshal(unit)
		deltas += strings.Count(string(encoded), `"type":"model.delta"`)
	}
	// 2wv (e): a token's own timeline entry stays on the PC.
	if deltas > 0 {
		t.Fatalf("%d model.delta timeline entries reached the device", deltas)
	}
	t.Logf("%d tokens over %s: %d patch frames (%d with text), %d bytes; last text %s after the last token", tokens, duration.Round(time.Millisecond), len(units), text, deviceUnitBytes(units), last.Sub(ended).Round(time.Millisecond))
	if limit := int(duration/deviceStreamTick) + 2; text > limit {
		t.Fatalf("%d text frames for %d tokens; at most %d", text, tokens, limit)
	}
	// One tick, and the scheduler's few milliseconds on a loaded test machine.
	limit := deviceStreamTick + 30*time.Millisecond
	if raceDetector {
		limit = raceTickAllowance
	}
	if late := last.Sub(ended); late > limit {
		t.Fatalf("the last token reached the device %s after the PC had it; at most %s", late, limit)
	}
}

func TestARunStateChangeKeepsItsPlaceAmongMergedText2wu(t *testing.T) {
	units, _, _ := streamStandInReplyToDevice(t, 40, 2*time.Millisecond, func(index int, chat string, bus *events.Bus) {
		if index == 19 {
			bus.Publish(events.New(events.ApprovalRequired, chat, "r1", map[string]any{"call_id": "c1"}))
		}
	})
	paused := slices.IndexFunc(units, func(unit map[string]any) bool {
		encoded, _ := json.Marshal(unit)
		return strings.Contains(string(encoded), `"status":"paused"`)
	})
	if paused < 0 {
		t.Fatal("the device never saw the run pause")
	}
	before, after := deviceText(units[:paused]), deviceText(units[paused+1:])
	var want strings.Builder
	for index := 0; index < 20; index++ {
		fmt.Fprintf(&want, "t%d ", index)
	}
	if before != want.String() || !strings.HasPrefix(after, "t20 ") {
		t.Fatalf("before the pause %q; after it %q", before, after)
	}
}

// The operator's own stream is not merged: one patch frame per token still.
func TestThePCStreamStillSendsOnePatchPerToken2wu(t *testing.T) {
	server, registry, writers, _, _, root := consoleServer(t)
	defer writers.Close()
	server.bus.SetSink(nil)
	server.bus.SetDurableSink(writers.WriteRecord, server.projector.Apply, server.projector.MarkStale)
	chat, err := registry.Create("streamed", server.ConfigSnapshot().DefaultAgentID(), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	page := &controlledSSEWriter{header: make(http.Header), entered: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		server.sse(page, httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx))
		close(done)
	}()
	<-page.entered
	server.bus.Publish(events.New(events.ModelRequest, chat.ID, "r1", map[string]any{"turn": 1}))
	const tokens = 200
	for index := 0; index < tokens; index++ {
		server.bus.Publish(events.New(events.ModelDelta, chat.ID, "r1", map[string]any{"turn": 1, "kind": "content", "text": "x"}))
	}
	count := 0
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if count = strings.Count(string(page.Bytes()), `"path":"/chat/turn:r1:1/text"`); count >= tokens {
			break
		}
	}
	cancel()
	<-done
	if count != tokens {
		t.Fatalf("the PC's stream carried %d text appends for %d tokens", count, tokens)
	}
}

// Item 2wv (c): every row a chat's Settings "Current session" list shows toggles
// through the route the switch posts to, and the list is the whole toolset.
func TestEveryCurrentSessionToolToggles2wv(t *testing.T) {
	server, registry, writers, _, _, root := consoleServer(t)
	defer writers.Close()
	chat, err := registry.Create("tools", server.ConfigSnapshot().DefaultAgentID(), root)
	if err != nil {
		t.Fatal(err)
	}
	listed, refused := []string{}, []string{}
	for _, tool := range chat.Snapshot().Tools {
		listed = append(listed, tool.Name)
		request := httptest.NewRequest(http.MethodPost, "/api/tools/"+tool.Name, strings.NewReader(`{"session_id":"`+chat.ID+`","enabled":`+fmt.Sprint(!tool.Enabled)+`}`))
		answer := httptest.NewRecorder()
		server.toggleTool(answer, request)
		if answer.Code != http.StatusOK {
			refused = append(refused, fmt.Sprintf("%s=%d", tool.Name, answer.Code))
		}
	}
	if len(refused) > 0 || !slices.Equal(listed, config.FullToolset()) {
		t.Fatalf("listed %v; refused %v", listed, refused)
	}
}

// Item 2wx (a): a paired device's state answer carries the listed keys and no
// others; the document states the same list.
func TestADevicesStateCarriesOnlyTheListedKeys2wx(t *testing.T) {
	server := dispatchServer(t)
	answer := dispatch(t, server, `{"v":1,"kind":"request","id":"ab","route":"state","body":{}}`)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(answer.Body, &body); err != nil {
		t.Fatal(err)
	}
	keys := slices.Sorted(maps.Keys(body))
	if !slices.Equal(keys, slices.Sorted(slices.Values(deviceStateKeys))) {
		t.Fatalf("a device's state carries %v", keys)
	}
	document, err := os.ReadFile(filepath.Join("..", "..", "docs", "app-message-v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	listed := "A device's `state` carries these keys and no others: `" + strings.Join(deviceStateKeys, "`, `") + "`."
	if !strings.Contains(string(document), listed) {
		t.Fatalf("docs/app-message-v1.md does not say %q", listed)
	}
}

// 2wy's end to end: 260 chats, because the fixture costs ~19 ms a chat on
// Windows; the 3,000-chat walk is TestTheDeviceListWalksThreeThousandChats2wy,
// and the 3,000-chat bytes were measured once with chats raised (NOTES.md).
func TestADevicesChatListArrivesRecentFirstAndPages2wy(t *testing.T) {
	chats := 260
	server := measuredServer(t, chats, 1)
	for chat := 0; chat < 5; chat++ {
		id := fmt.Sprintf("chat-%d", chat)
		if chat < 3 {
			server.bus.Publish(events.New(events.RunStarted, id, "busy", map[string]any{"run_id": "busy"}))
		} else {
			server.bus.Publish(events.New(events.ApprovalRequired, id, "busy", map[string]any{"call_id": "c", "name": "shell"}))
		}
	}
	live, device := newPhoneStreamState(), &recordingDevice{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { server.streamUnitsToDeviceOpening(ctx, device, nil, live); close(done) }()
	defer func() { cancel(); <-done }()
	listed := device.waitFor(t, "the list snapshot", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		return data["type"] == events.ChatListSnapshot
	})
	payload := listed["data"].(map[string]any)["data"].(map[string]any)
	joined := map[string]bool{}
	for count := -1; ; time.Sleep(300 * time.Millisecond) {
		device.mu.Lock()
		settled := len(device.units) == count
		count = len(device.units)
		device.mu.Unlock()
		if settled {
			break
		}
	}
	device.mu.Lock()
	joinBytes := 0
	for _, unit := range device.units {
		encoded, _ := appCanonical(unit)
		joinBytes += len(encoded)
		if unit["kind"] == "snapshot" {
			joined[unit["session_id"].(string)] = true
		}
	}
	device.mu.Unlock()
	t.Logf("2wy join of %d chats: %d rows, %d bytes", chats, len(joined), joinBytes)
	if payload["total"] != float64(chats) {
		t.Fatalf("the list snapshot = %v", payload)
	}
	for chat := 0; chat < 5; chat++ {
		if !joined[fmt.Sprintf("chat-%d", chat)] {
			t.Fatalf("running or waiting chat-%d was not joined", chat)
		}
	}
	if len(joined) != 5+deviceListPage {
		t.Fatalf("the join sent %d rows", len(joined))
	}

	var state struct {
		Sessions map[string]projection.Snapshot `json:"sessions"`
		Total    int                            `json:"total"`
		Next     string                         `json:"next"`
	}
	answer := dispatch(t, server, `{"v":1,"kind":"request","id":"s","route":"state","body":{}}`)
	if err := json.Unmarshal(answer.Body, &state); err != nil || len(state.Sessions) != 5+deviceListPage || state.Total != chats || state.Next != payload["next"] {
		t.Fatalf("a device's state = %d rows, total %d, next %q (%v)", len(state.Sessions), state.Total, state.Next, err)
	}
	t.Logf("2wy device state of %d chats: %d rows, %d bytes", chats, len(state.Sessions), len(answer.Body))

	seen := maps.Clone(joined)
	// A chat outside the loaded page that starts running reaches the device as a row.
	outside := ""
	for chat := chats - 1; outside == ""; chat-- {
		if id := fmt.Sprintf("chat-%d", chat); !joined[id] {
			outside = id
		}
	}
	// 2wz: its row carries the folder and activity a join row carries.
	chatStore := chatstore.New(t.TempDir())
	server.chatState.store = chatStore
	if _, err := chatStore.AddFolder("", "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := chatStore.Create(outside, outside, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := chatStore.Move(outside, "work"); err != nil {
		t.Fatal(err)
	}
	if err := chatStore.NoteActivity(outside, time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC), nil, nil); err != nil {
		t.Fatal(err)
	}
	server.bus.Publish(events.New(events.ChatListPatch, "", "", map[string]any{"operation": "move", "session_id": outside, "folder": "work"}))
	device.waitFor(t, "the move", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		payload, _ := data["data"].(map[string]any)
		return payload["operation"] == "move" && payload["session_id"] == outside
	})
	server.bus.Publish(events.New(events.RunStarted, outside, "late", map[string]any{"run_id": "late"}))
	var row map[string]any
	device.waitFor(t, "the outside chat's row", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		payload, _ := data["data"].(map[string]any)
		row, _ = payload["session"].(map[string]any)
		return data["type"] == events.ChatListPatch && payload["operation"] == "upsert" && row["id"] == outside
	})
	rows := server.projector.CurrentSnapshot()
	server.decorateChatList(rows)
	if want := rows[outside]; want.Folder != "work" || want.LastActivity == "" || row["folder"] != want.Folder || row["last_activity"] != want.LastActivity {
		t.Fatalf("the upsert row carried folder %v, last_activity %v; a join row carries %q, %q", row["folder"], row["last_activity"], want.Folder, want.LastActivity)
	}
	seen[outside] = true
	server.bus.Publish(events.New(events.SessionRenamed, outside, "", map[string]any{"label": "renamed outside"}))
	device.waitFor(t, "the outside chat's title", func(unit map[string]any) bool {
		data, _ := unit["data"].(map[string]any)
		return unit["kind"] == "patch" && data["session_id"] == outside && strings.Contains(fmt.Sprint(data["operations"]), "renamed outside")
	})

	before, pages := state.Next, 0
	for before != "" {
		body, _ := json.Marshal(map[string]any{"before": before, "n": deviceListPage})
		var page struct {
			Sessions map[string]projection.Snapshot `json:"sessions"`
			Next     *string                        `json:"next"`
		}
		raw := server.dispatchAppMessage("device-1", []byte(`{"v":1,"kind":"request","id":"p","route":"chat.list.page","body":`+string(body)+`}`), live)
		var response appResponseUnit
		if json.Unmarshal(raw, &response) != nil || response.Status != http.StatusOK || json.Unmarshal(response.Body, &page) != nil {
			t.Fatalf("chat.list.page = %s", raw)
		}
		if pages++; page.Next != nil && len(page.Sessions) != deviceListPage || len(page.Sessions) > deviceListPage {
			t.Fatalf("page %d carried %d rows", pages, len(page.Sessions))
		}
		for id, row := range page.Sessions {
			if seen[id] {
				t.Fatalf("page %d repeated %s", pages, id)
			}
			if row.Chat != nil {
				t.Fatalf("page %d carried %s's transcript", pages, id)
			}
			seen[id] = true
		}
		if before = ""; page.Next != nil {
			before = *page.Next
		}
	}
	if len(seen) != chats {
		t.Fatalf("the pages walked %d of %d chats", len(seen), chats)
	}
}

func TestTheDeviceListWalksThreeThousandChats2wy(t *testing.T) {
	sessions := map[string]projection.Snapshot{}
	base := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for chat := 0; chat < 3000; chat++ {
		value := projection.Snapshot{ID: fmt.Sprintf("chat-%d", chat), LastActivity: base.Add(time.Duration(chat%1500) * time.Second).Format(time.RFC3339Nano)}
		switch chat {
		case 0, 1, 2:
			value.Run.Status = "running"
		case 3, 4:
			value.PendingApproval = &projection.ChatEntry{Type: "notice"}
		}
		sessions[value.ID] = value
	}
	page, next, ok := deviceChatPage(sessions, "", "", deviceListPage)
	if !ok || len(page) != 205 || next == "" {
		t.Fatalf("the first page = %d rows, next %q", len(page), next)
	}
	seen := maps.Clone(page)
	last := next
	for pages := 1; next != ""; pages++ {
		page, next, ok = deviceChatPage(sessions, "", next, deviceListPage)
		if !ok || pages <= 3 && len(page) != deviceListPage {
			t.Fatalf("page %d = %d rows", pages, len(page))
		}
		for id, value := range page {
			if _, repeated := seen[id]; repeated || value.LastActivity+"|"+id >= last && value.LastActivity != base.Add(1499*time.Second).Format(time.RFC3339Nano) {
				t.Fatalf("page %d repeated or reordered %s", pages, id)
			}
			seen[id] = value
		}
		if next != "" {
			last = next
		}
	}
	if len(seen) != 3000 {
		t.Fatalf("the pages walked %d of 3000 chats", len(seen))
	}
	if _, _, ok := deviceChatPage(sessions, "", "not a cursor", deviceListPage); ok {
		t.Fatal("a cursor this desktop did not give was taken")
	}
}
