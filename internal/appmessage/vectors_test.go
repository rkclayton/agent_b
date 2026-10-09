// Package appmessage holds no code. It holds the normative vectors for
// `agentb-app-message-v1` — the plaintext inside the broker's CIPHERTEXT — and
// the gate that fails when those vectors and `docs/app-message-v1.md` disagree.
//
// Item 2ml. The iOS app shipped an invented shape named
// `agentb-ios-provisional-v0` because this document did not exist; the broker's
// own specification says the application payload "is versioned separately and
// the broker MUST NOT parse or validate it", which is the seam this fills.
// Nothing here opens a socket or speaks to a broker: that is 2kq, ordered later
// and against this document.
package appmessage

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	vectorPath   = "testdata/vectors-v1.json"
	documentPath = "../../docs/app-message-v1.md"
)

type vector struct {
	Name       string          `json:"name"`
	Note       string          `json:"note"`
	Decoded    json.RawMessage `json:"decoded"`
	Bytes      string          `json:"bytes"`
	ByteLength int             `json:"byte_length"`
	SHA256     string          `json:"sha256"`
}

type vectorFile struct {
	Version         int      `json:"version"`
	Document        string   `json:"document"`
	UnitMax         int      `json:"unit_max"`
	DownstreamKinds []string `json:"downstream_kinds"`
	Routes          []string `json:"routes"`
	Vectors         []vector `json:"vectors"`
	Split           struct {
		Name       string   `json:"name"`
		UnitMax    int      `json:"unit_max"`
		ChunkBytes int      `json:"chunk_bytes"`
		Unit       vector   `json:"unit"`
		Parts      []vector `json:"parts"`
	} `json:"split"`
}

// canonical is the encoding the document names: compact JSON with object keys
// sorted by code point, which is exactly what encoding/json does for a map.
func canonical(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode: %v", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

func load(t *testing.T) (vectorFile, string) {
	t.Helper()
	raw, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	document, err := os.ReadFile(filepath.FromSlash(documentPath))
	if err != nil {
		t.Fatalf("read document: %v", err)
	}
	return file, string(document)
}

// Every vector's exact bytes are re-derived from its decoded value, so a hand
// edit to either side fails here rather than reaching another repository.
func TestVectorBytesAreRederived(t *testing.T) {
	file, _ := load(t)
	all := append([]vector{}, file.Vectors...)
	all = append(all, file.Split.Unit)
	all = append(all, file.Split.Parts...)
	if len(all) < 7 {
		t.Fatalf("expected the five vector kinds plus the split unit and its parts, got %d entries", len(all))
	}
	for _, entry := range all {
		name := entry.Name
		if name == "" {
			name = "split part"
		}
		if got := canonical(t, entry.Decoded); got != entry.Bytes {
			t.Errorf("%s: bytes are not the canonical encoding of decoded\n  want %s\n  got  %s", name, entry.Bytes, got)
		}
		if got := len([]byte(entry.Bytes)); got != entry.ByteLength {
			t.Errorf("%s: byte_length %d, bytes are %d", name, entry.ByteLength, got)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256([]byte(entry.Bytes))); got != entry.SHA256 {
			t.Errorf("%s: sha256 %s, digest of bytes is %s", name, entry.SHA256, got)
		}
	}
}

// The five kinds item 2ml (f) requires, each present and each carrying what the
// document says it carries.
func TestTheFiveVectorKindsArePresent(t *testing.T) {
	file, _ := load(t)
	byName := map[string]vector{}
	for _, entry := range file.Vectors {
		byName[entry.Name] = entry
	}
	for _, name := range []string{"snapshot", "patch", "request", "response", "refusal"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("vector %q is missing", name)
		}
	}
	field := func(entry vector, path ...string) any {
		var value map[string]any
		if err := json.Unmarshal(entry.Decoded, &value); err != nil {
			t.Fatalf("%s: %v", entry.Name, err)
		}
		var current any = value
		for _, key := range path {
			object, ok := current.(map[string]any)
			if !ok {
				t.Fatalf("%s: %s is not an object", entry.Name, key)
			}
			current = object[key]
		}
		return current
	}
	for name, entry := range byName {
		if field(entry, "v") != float64(1) {
			t.Errorf("%s: v is not 1", name)
		}
	}
	// (d): the response names its request's id, and carries the route's status.
	if field(byName["response"], "id") != field(byName["request"], "id") {
		t.Error("the response does not name the request's id")
	}
	if field(byName["response"], "status") != float64(202) {
		t.Error("the request/response pair does not carry POST /api/message's 202")
	}
	// A refusal is a response, not an error frame.
	if field(byName["refusal"], "kind") != "response" {
		t.Error("the refusal is not carried as a response")
	}
	if field(byName["refusal"], "status") != float64(409) {
		t.Error("the refusal does not carry the route's own status")
	}
	if text, _ := field(byName["refusal"], "body", "error").(string); text == "" {
		t.Error("the refusal names no reason")
	}
	// The snapshot and patch carry the projection's cursor, which is what (c)'s
	// resume rule depends on.
	if field(byName["snapshot"], "data", "cursor", "generation") == nil {
		t.Error("the snapshot carries no cursor generation")
	}
	if field(byName["patch"], "data", "previous_cursor") == nil {
		t.Error("the patch carries no previous_cursor, so a gap could not be detected")
	}
}

// (c): the oversize unit splits, every part fits the budget in force, and the
// parts reassemble to exactly the whole unit's bytes.
func TestOversizeUnitSplitsAndReassembles(t *testing.T) {
	file, _ := load(t)
	split := file.Split
	if split.UnitMax <= 0 || split.UnitMax >= file.UnitMax {
		t.Fatalf("the split vector's reduced budget %d is not below the real ceiling %d", split.UnitMax, file.UnitMax)
	}
	if split.Unit.ByteLength <= split.UnitMax {
		t.Fatalf("the unit is %d bytes and the budget is %d: nothing would be split", split.Unit.ByteLength, split.UnitMax)
	}
	if len(split.Parts) < 2 {
		t.Fatalf("a split needs at least two parts, got %d", len(split.Parts))
	}
	var reassembled []byte
	for index, part := range split.Parts {
		if part.ByteLength > split.UnitMax {
			t.Errorf("part %d encodes to %d bytes, over the %d budget", index, part.ByteLength, split.UnitMax)
		}
		var decoded struct {
			Kind       string `json:"kind"`
			UnitID     string `json:"unit_id"`
			Index      int    `json:"index"`
			Count      int    `json:"count"`
			TotalBytes int    `json:"total_bytes"`
			SHA256     string `json:"sha256"`
			Chunk      string `json:"chunk"`
		}
		if err := json.Unmarshal(part.Decoded, &decoded); err != nil {
			t.Fatalf("part %d: %v", index, err)
		}
		if decoded.Kind != "part" {
			t.Errorf("part %d is kind %q", index, decoded.Kind)
		}
		if decoded.Index != index {
			t.Errorf("part at position %d declares index %d: the order is the rule", index, decoded.Index)
		}
		if decoded.Count != len(split.Parts) {
			t.Errorf("part %d declares count %d, there are %d parts", index, decoded.Count, len(split.Parts))
		}
		if decoded.TotalBytes != split.Unit.ByteLength || decoded.SHA256 != split.Unit.SHA256 {
			t.Errorf("part %d's total_bytes/sha256 do not name the whole unit", index)
		}
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(decoded.UnitID) {
			t.Errorf("part %d: unit_id %q is not 32 lower-case hex", index, decoded.UnitID)
		}
		chunk, err := base64.RawURLEncoding.DecodeString(decoded.Chunk)
		if err != nil {
			t.Fatalf("part %d: chunk is not unpadded base64url: %v", index, err)
		}
		reassembled = append(reassembled, chunk...)
	}
	if string(reassembled) != split.Unit.Bytes {
		t.Fatalf("the parts do not reassemble to the unit: %d bytes against %d", len(reassembled), split.Unit.ByteLength)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(reassembled)); got != split.Unit.SHA256 {
		t.Fatalf("reassembled digest %s, declared %s", got, split.Unit.SHA256)
	}
}

// The gate item 2ml (f) asks for: the document and the vectors cannot drift.
// Every kind and every route the vectors use must be named in the document, and
// every route the document publishes must be declared by the vectors — so
// adding a route to one and not the other fails here.
func TestDocumentAndVectorsAgree(t *testing.T) {
	file, document := load(t)
	document = strings.ReplaceAll(document, "\r\n", "\n")
	if !strings.Contains(document, file.Document) && !strings.Contains(file.Document, "app-message-v1.md") {
		t.Errorf("the vectors name document %q", file.Document)
	}
	if !strings.Contains(document, "8 388 608") && !strings.Contains(document, "8388608") {
		t.Error("the document does not state the 8 MiB ceiling in bytes")
	}
	for _, kind := range file.DownstreamKinds {
		if !strings.Contains(document, "`"+kind+"`") {
			t.Errorf("kind %q is not named in the document", kind)
		}
	}
	// The document's route table, read from the table rows themselves.
	// Item 2my: a route name may carry a dot now (`chat.create`), so the pattern admits
	// one. It stays strict otherwise: the route set is closed and a name is still only
	// lower-case letters and dots, never a path.
	start := strings.Index(document, "| `route` |")
	if start < 0 {
		t.Fatal("the route table heading is missing")
	}
	end := strings.Index(document[start:], "\n\n")
	if end < 0 {
		t.Fatal("the route table does not end")
	}
	routeTable := document[start : start+end]
	rows := regexp.MustCompile("(?m)^\\| `([a-z.]+)` \\|").FindAllStringSubmatch(routeTable, -1)
	published := map[string]bool{}
	for _, row := range rows {
		if row[1] == "route" {
			continue
		}
		published[row[1]] = true
	}
	if len(published) == 0 {
		t.Fatal("no route table was found in the document")
	}
	for _, route := range file.Routes {
		if !published[route] {
			t.Errorf("the vectors declare route %q, which the document's table does not publish", route)
		}
	}
	for route := range published {
		found := false
		for _, declared := range file.Routes {
			if declared == route {
				found = true
			}
		}
		if !found {
			t.Errorf("the document publishes route %q, which the vectors do not declare", route)
		}
	}
	// (e): the list of what a device may NOT reach is a security statement, so
	// its presence is asserted rather than assumed.
	for _, refused := range []string{"/api/config", "/api/service-account", "/api/hardening", "/api/update", "shell.operator_context"} {
		if !strings.Contains(document, refused) {
			t.Errorf("the document does not list %q among what a device cannot reach", refused)
		}
	}
	// (g): nothing here builds toward the broker.
	if entries, err := os.ReadDir("."); err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
				t.Errorf("%s: this package is a specification and its vectors, not a client", entry.Name())
			}
		}
	}
}

// A seeded disagreement must fail. This runs the same re-derivation the first
// test runs, against a deliberately corrupted copy, and fails if it PASSES.
func TestASeededDisagreementFails(t *testing.T) {
	file, _ := load(t)
	corrupted := file.Vectors[0]
	corrupted.Bytes = strings.Replace(corrupted.Bytes, `"complete":true`, `"complete":false`, 1)
	if corrupted.Bytes == file.Vectors[0].Bytes {
		t.Fatal("the seed did not change the vector, so this test proves nothing")
	}
	if canonical(t, corrupted.Decoded) == corrupted.Bytes {
		t.Fatal("a corrupted vector still matched its decoded value: the gate would not catch a drift")
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(corrupted.Bytes))) == corrupted.SHA256 {
		t.Fatal("a corrupted vector still matched its digest")
	}
}

// Items 2my, 2ow, 2qc, and 2qk: the explicitly published chat routes do not widen
// the rest of the lifecycle surface.
//
// The route set is a security statement, so the thing worth testing is not that
// The route names are the authority: adding rename and delete must not quietly bring
// close, move, archive, or a generic session path with them.
func TestOnlyPublishedChatRoutesWereAddedAndTheRestStayRefused2my(t *testing.T) {
	file, document := load(t)

	// The closed set, exactly.
	want := []string{"message", "stop", "approve", "tool", "state", "resync", "chat.create", "chat.history", "chat.mirror", "chat.mirror.since", "chat.mirror.take", "chat.rename", "chat.delete"}
	if len(file.Routes) != len(want) {
		t.Fatalf("the route set is %v, want %v", file.Routes, want)
	}
	for index, route := range want {
		if file.Routes[index] != route {
			t.Fatalf("route %d is %q, want %q; the five original routes must stay byte-for-byte in place", index, file.Routes[index], route)
		}
	}

	// 2ow added connection_id and 2s6 adds agent_id. Role/source copying remains
	// unavailable, while both safe selection sheets are exact.
	for _, wanted := range []string{
		"{label?,agent_id?,connection_id?}", "It still cannot carry `role` or", "`source_session_id`",
		"`{id,label,model,host,vision,docs,tools,ctx}`", "no URL path, credential, API key",
	} {
		if !strings.Contains(document, wanted) {
			t.Errorf("the connection-selection contract does not say %q", wanted)
		}
	}

	// (c): the lifecycle routes other than those named are still refused, and the exposure is
	// stated rather than left for a reader to infer.
	for _, wanted := range []string{
		"every session-lifecycle route EXCEPT",
		"reopen, move, archive",
		"The exposure `chat.create` adds",
		"The exposure `chat.rename` and `chat.delete` add",
		"open empty chats",
	} {
		if !strings.Contains(document, wanted) {
			t.Errorf("the document does not say %q", wanted)
		}
	}
	// It must NOT claim a phone cannot create a chat any more.
	if strings.Contains(document, "create or delete a chat") {
		t.Error("the refused list still claims a phone cannot create a chat")
	}

	// (d): version stays 1, and the addition is dated.
	if file.Version != 1 {
		t.Errorf("version = %d; an additive route is compatible and must not bump it", file.Version)
	}
	if !strings.Contains(document, "connection selection added 2026-09-30") {
		t.Error("the added route is not dated in the table")
	}
	if !strings.Contains(document, "agent selection added 2026-10-09") {
		t.Error("the agent addition is not dated in the table")
	}
	if !strings.Contains(document, "**Version stays 1.**") {
		t.Error("the document does not say why the version is unchanged")
	}

	// (b): the pair is the request and its 201, under one id.
	var request, response *vector
	for index := range file.Vectors {
		switch file.Vectors[index].Name {
		case "chat.create request":
			request = &file.Vectors[index]
		case "chat.create response":
			response = &file.Vectors[index]
		}
	}
	if request == nil || response == nil {
		t.Fatal("the request/201 pair is not in the vectors")
	}
	var decodedRequest, decodedResponse map[string]any
	if err := json.Unmarshal(request.Decoded, &decodedRequest); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(response.Decoded, &decodedResponse); err != nil {
		t.Fatal(err)
	}
	if decodedRequest["id"] != decodedResponse["id"] {
		t.Error("the response does not name the request's id")
	}
	if status, _ := decodedResponse["status"].(float64); status != 201 {
		t.Errorf("the response status is %v, want 201", decodedResponse["status"])
	}
	body, _ := decodedRequest["body"].(map[string]any)
	if len(body) != 1 || body["label"] == nil {
		t.Errorf("the request body is %v; it carries a label and nothing else", body)
	}
}
