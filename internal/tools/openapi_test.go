package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/session"
)

// Item 2nr: a connector imports its OpenAPI document, and the model calls the operations
// the operator enabled BY NAME. Every fixture here is synthetic — no real hostname,
// operation name, key or data (2m9) — and nothing leaves this machine: the document and
// the API are both a local stub.

// The synthetic document: OpenAPI 3.1 JSON, three operations, one local $ref, one enum,
// one JSON body, and a security scheme that takes a key in a header.
const syntheticDocument = `{
  "openapi": "3.1.0",
  "info": {"title": "Widget Depot", "version": "1"},
  "components": {
    "parameters": {
      "WidgetId": {"name": "widget_id", "in": "path", "required": true, "schema": {"type": "string"}, "description": "The widget"}
    },
    "securitySchemes": {"depotKey": {"type": "apiKey", "in": "header", "name": "X-Depot-Key"}}
  },
  "security": [{"depotKey": []}],
  "paths": {
    "/widgets": {
      "get": {
        "operationId": "listWidgets",
        "summary": "List the widgets on a shelf",
        "parameters": [
          {"name": "shelf", "in": "query", "required": true, "schema": {"type": "string"}},
          {"name": "limit", "in": "query", "schema": {"type": "integer"}, "description": "How many"},
          {"name": "sort", "in": "query", "schema": {"type": "string", "enum": ["name", "age"]}}
        ]
      },
      "post": {
        "operationId": "createWidget",
        "summary": "Add a widget",
        "requestBody": {"required": true, "content": {"application/json": {"schema": {
          "type": "object", "required": ["name"],
          "properties": {"name": {"type": "string"}, "spare": {"type": "boolean", "description": "Kept in reserve"}}
        }}}}
      }
    },
    "/widgets/{widget_id}": {
      "get": {
        "operationId": "getWidget",
        "summary": "Read one widget",
        "parameters": [{"$ref": "#/components/parameters/WidgetId"}]
      }
    }
  }
}`

// (b) the document becomes operations the card can show, and one line about what its own
// auth wants — which is shown, never used: (f) says the auth stays the connector's.
func TestTheDocumentBecomesOperationsAndOneAuthLine2nr(t *testing.T) {
	document, err := ParseServiceDocument([]byte(syntheticDocument))
	if err != nil {
		t.Fatal(err)
	}
	if document.Version != "3.1.0" {
		t.Errorf("version %q", document.Version)
	}
	if !strings.Contains(document.Auth, "X-Depot-Key") || !strings.Contains(strings.ToLower(document.Auth), "header") {
		t.Errorf("the auth line does not say what the document wants: %q", document.Auth)
	}
	ids := []string{}
	for _, operation := range document.Operations {
		ids = append(ids, operation.ID)
	}
	if strings.Join(ids, ",") != "listWidgets,createWidget,getWidget" {
		t.Fatalf("operations %v", ids)
	}
	list := document.Operations[0]
	if list.Method != "GET" || list.Path != "/widgets" || list.Summary != "List the widgets on a shelf" {
		t.Fatalf("%+v", list)
	}
	if len(list.Params) != 3 || !list.Params[0].Required || list.Params[1].Type != "integer" || strings.Join(list.Params[2].Enum, ",") != "name,age" {
		t.Fatalf("%+v", list.Params)
	}
	// A local $ref is resolved rather than carried.
	one := document.Operations[2]
	if len(one.Params) != 1 || one.Params[0].Name != "widget_id" || one.Params[0].In != "path" || !one.Params[0].Required {
		t.Fatalf("%+v", one.Params)
	}
	// The body's properties are parameters like any other, in "body".
	create := document.Operations[1]
	if len(create.Params) != 2 || create.Params[0].In != "body" || !create.Params[0].Required || create.Params[1].Required {
		t.Fatalf("%+v", create.Params)
	}
	// Anything that is not 3.0 or 3.1 JSON is refused with one line, and YAML is refused
	// by the same line rather than by a new dependency.
	for _, bad := range []string{`{"openapi":"2.0","paths":{}}`, "openapi: 3.1.0\npaths: {}\n"} {
		if _, err := ParseServiceDocument([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// The stub API and a connector that has imported the document, with the named operations
// enabled. Returns the tool, a pointer to the count of requests the stub saw, and the
// snapshot's path.
func documentConnector(t *testing.T, enabled ...string) (*CallService, *int, string) {
	t.Helper()
	reached := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		raw, _ := json.Marshal(map[string]any{"ok": true, "path": r.URL.Path, "query": r.URL.RawQuery, "method": r.Method})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(stub.Close)

	root := t.TempDir()
	snapshot := filepath.Join(root, "connectors", "depot", "openapi.json")
	if err := os.MkdirAll(filepath.Dir(snapshot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot, []byte(syntheticDocument), 0o600); err != nil {
		t.Fatal(err)
	}
	service := testService(stub.URL, "none")
	service.AllowedMethods = []string{"GET", "POST"}
	service.OpenAPI = &config.ServiceOpenAPI{
		Source:     stub.URL + "/openapi.json",
		Snapshot:   snapshot,
		SHA256:     DocumentDigest([]byte(syntheticDocument)),
		Operations: enabled,
	}
	tool := NewCallService(map[string]config.Service{"depot": service})
	return tool, &reached, snapshot
}

// The acceptance's second case: the call reaches the stub with the document's method and
// path, filled from params, and the result shape is the existing one.
func TestAnOperationCallReachesTheStubAsTheDocumentSaysIt2nr(t *testing.T) {
	tool, _, _ := documentConnector(t, "listWidgets", "getWidget")
	result := callServiceResult(t, tool, map[string]any{
		"service": "depot", "operation": "listWidgets",
		"params": map[string]any{"shelf": "north", "limit": 5, "sort": "age"},
	})
	if result["status"] != float64(200) {
		t.Fatalf("result %+v", result)
	}
	if _, present := result["duration_ms"]; !present {
		t.Fatalf("the result shape changed: %+v", result)
	}
	// The body comes back decoded, as it does for any JSON answer: the shape is the
	// existing one and this changes none of it.
	seen, _ := result["body"].(map[string]any)
	if seen["path"] != "/widgets" || seen["method"] != "GET" {
		t.Fatalf("the stub saw %+v", seen)
	}
	query, _ := seen["query"].(string)
	for _, wanted := range []string{"shelf=north", "limit=5", "sort=age"} {
		if !strings.Contains(query, wanted) {
			t.Errorf("the query %q is missing %q", query, wanted)
		}
	}

	// A path parameter goes into the path, not the query.
	one := callServiceResult(t, tool, map[string]any{
		"service": "depot", "operation": "getWidget", "params": map[string]any{"widget_id": "a-1"},
	})
	if body, _ := one["body"].(map[string]any); body["path"] != "/widgets/a-1" {
		t.Fatalf("the stub saw %+v", body)
	}
}

// (c) THE DOCUMENT IS THE ALLOW-LIST.
func TestTheDocumentIsTheAllowList2nr(t *testing.T) {
	tool, reached, _ := documentConnector(t, "listWidgets", "getWidget")
	item := &session.Session{}
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{"not enabled", map[string]any{"service": "depot", "operation": "createWidget", "params": map[string]any{"name": "x"}}},
		{"unknown", map[string]any{"service": "depot", "operation": "dropDatabase"}},
		{"raw call", map[string]any{"service": "depot", "method": "GET", "path": "widgets"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := tool.Call(context.Background(), item, test.args)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), "listWidgets") || !strings.Contains(err.Error(), "getWidget") {
				t.Fatalf("the refusal does not list the operations: %v", err)
			}
		})
	}
	if *reached != 0 {
		t.Fatalf("%d refused call(s) reached the stub", *reached)
	}
}

// (e) PARAMETERS ARE CHECKED BEFORE DIALING: each refusal names the parameter, and
// nothing is sent.
func TestParametersAreCheckedBeforeDialing2nr(t *testing.T) {
	tool, reached, _ := documentConnector(t, "listWidgets", "createWidget")
	item := &session.Session{}
	for _, test := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing", map[string]any{"service": "depot", "operation": "listWidgets", "params": map[string]any{}}, "shelf"},
		{"unknown", map[string]any{"service": "depot", "operation": "listWidgets", "params": map[string]any{"shelf": "north", "colour": "red"}}, "colour"},
		{"type", map[string]any{"service": "depot", "operation": "listWidgets", "params": map[string]any{"shelf": "north", "limit": "many"}}, "limit"},
		{"enum", map[string]any{"service": "depot", "operation": "listWidgets", "params": map[string]any{"shelf": "north", "sort": "colour"}}, "name, age"},
		{"body", map[string]any{"service": "depot", "operation": "createWidget", "params": map[string]any{"spare": true}}, "name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := tool.Call(context.Background(), item, test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want it to name %q", err, test.want)
			}
		})
	}
	if *reached != 0 {
		t.Fatalf("%d refused call(s) reached the stub", *reached)
	}
}

// (d) THE MODEL SEES THE OPERATIONS, and a connector without a document is untouched.
func TestTheDefinitionListsTheEnabledOperations2nr(t *testing.T) {
	tool, _, _ := documentConnector(t, "listWidgets", "getWidget")
	schema, err := json.Marshal(tool.Schema())
	if err != nil {
		t.Fatal(err)
	}
	text := string(schema)
	for _, wanted := range []string{"listWidgets", "getWidget", "GET /widgets", "shelf", "integer", "name, age"} {
		if !strings.Contains(text, wanted) {
			t.Errorf("the definition does not carry %q", wanted)
		}
	}
	if strings.Contains(text, "createWidget") {
		t.Error("the definition offers an operation the operator did not enable")
	}
	plain := NewCallService(map[string]config.Service{"plain": testService("https://example.test", "none")})
	before, _ := json.Marshal(NewCallService(nil).Schema())
	after, _ := json.Marshal(plain.Schema())
	if string(before) != string(after) {
		t.Errorf("a connector without a document changed the definition:\n%s\n%s", before, after)
	}
}

// (g) a snapshot that no longer matches its hash refuses the connector's operations.
func TestAnEditedSnapshotRefusesTheOperations2nr(t *testing.T) {
	tool, _, snapshot := documentConnector(t, "listWidgets")
	edited := strings.Replace(syntheticDocument, "/widgets", "/everything", 1)
	if err := os.WriteFile(snapshot, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := tool.Call(context.Background(), &session.Session{}, map[string]any{
		"service": "depot", "operation": "listWidgets", "params": map[string]any{"shelf": "north"},
	})
	if err == nil || !strings.Contains(err.Error(), "changed since it was approved") {
		t.Fatalf("error=%v", err)
	}
}

// (b) the proposal carries the document and the operations, so the SAME card can show
// them; parsing does no I/O, and the fetch is the caller's.
func TestAConnectorProposalCarriesTheDocument2nr(t *testing.T) {
	change, present, err := ParseConnectorChange(map[string]any{"connector": map[string]any{
		"operation": "add",
		"entry": map[string]any{
			"name": "depot", "url": "https://depot.invalid", "kind": "http",
			"auth": "stored:depot-key", "allowed_methods": []any{"GET"},
			"openapi": map[string]any{"document": "https://depot.invalid/openapi.json", "operations": []any{"listWidgets"}},
		},
	}})
	if !present || err != nil {
		t.Fatalf("present=%t err=%v", present, err)
	}
	if change.Service.OpenAPI == nil || change.Service.OpenAPI.Source != "https://depot.invalid/openapi.json" {
		t.Fatalf("%+v", change.Service.OpenAPI)
	}
	if strings.Join(change.Service.OpenAPI.Operations, ",") != "listWidgets" {
		t.Fatalf("%+v", change.Service.OpenAPI.Operations)
	}
	if change.Service.OpenAPI.SHA256 != "" || change.Service.OpenAPI.Snapshot != "" {
		t.Fatal("parsing reached for the document; the fetch belongs to the approval path")
	}
}

// The document is fetched once, over https — or over plain http only when it is on this
// machine, which is what makes a local stub testable without a certificate.
func TestTheDocumentIsFetchedOnceAndOnlyOverHTTPSOrLoopback2nr(t *testing.T) {
	fetches := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++
		_, _ = w.Write([]byte(syntheticDocument))
	}))
	defer stub.Close()
	for attempt := 0; attempt < 3; attempt++ {
		raw, document, err := LoadServiceDocument(context.Background(), stub.URL+"/openapi.json")
		if err != nil {
			t.Fatal(err)
		}
		if len(document.Operations) != 3 || DocumentDigest(raw) != DocumentDigest([]byte(syntheticDocument)) {
			t.Fatalf("%d operations", len(document.Operations))
		}
	}
	if fetches != 1 {
		t.Fatalf("the document was fetched %d times; the card and the approval share one fetch", fetches)
	}
	if _, _, err := LoadServiceDocument(context.Background(), "http://depot.invalid/openapi.json"); err == nil {
		t.Error("a plain http document on another machine was accepted")
	}
	if _, _, err := LoadServiceDocument(context.Background(), "relative/openapi.json"); err == nil {
		t.Error("a relative file path was accepted")
	}
}
