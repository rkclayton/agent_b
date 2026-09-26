package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/llm"
)

// Item 2l1 (b). The operator could not save because his connection carried the
// literal model "model", seeded by config.Defaults, and emptiness was the only
// check — so the placeholder reached save and was refused elsewhere, in words he
// could not act on.
func TestNoConnectionIsSeededWithAModelNamedModel2l1(t *testing.T) {
	seeded := config.Defaults(".")
	for _, connection := range seeded.Connections {
		if strings.TrimSpace(connection.Model) == "model" {
			t.Fatalf("connection %q is still seeded with the placeholder model", connection.ID)
		}
		if strings.TrimSpace(connection.Model) != "" {
			t.Fatalf("connection %q is seeded with a model at all: %q", connection.ID, connection.Model)
		}
		if reason := config.ConnectionSetupReason(&connection); !strings.Contains(reason, "model is empty") {
			t.Fatalf("the reason for a connection with no model is %q", reason)
		}
	}
}

// (a7). The operator's `server` connection hand-set n_ctx to 200000 while its
// probed n_ctx was 0, because vLLM answers 404 to the props route. vLLM publishes
// the real window on /v1/models as max_model_len — 262144 in his case — and the
// catalog reads it.
func TestThePublishedContextLengthIsRead2l1(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen3.8-flash-next","owned_by":"vllm","max_model_len":262144}]}`))
	}))
	defer server.Close()
	connection := &config.Connection{ID: "server", BaseURL: server.URL, Model: "qwen3.8-flash-next", RequestTimeoutS: 10}
	entries, err := llm.New(connection).ModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if len(entries) != 1 || entries[0].ContextLength != 262144 {
		t.Fatalf("the published window was not read: %+v", entries)
	}
	proposed := (&Server{}).proposedConnectionValues(context.Background(), connection, []string{"qwen3.8-flash-next"})
	if proposed["context_source"] != "published" {
		t.Fatalf("the window was not reported as published: %v", proposed)
	}
	if proposed["n_ctx"] != 262144 {
		t.Fatalf("proposed n_ctx %v, want 262144", proposed["n_ctx"])
	}
	if proposed["reserve_output"] != config.ReserveOutputFor(262144) {
		t.Fatalf("proposed reserve %v, want %d", proposed["reserve_output"], config.ReserveOutputFor(262144))
	}
}

// (a7) again: where nothing publishes a window the field says the value is
// unverified rather than presenting a guess as a measurement.
func TestAnUnpublishedWindowIsReportedUnverified2l1(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m"}]}`))
	}))
	defer server.Close()
	proposed := (&Server{}).proposedConnectionValues(context.Background(), &config.Connection{ID: "c", BaseURL: server.URL, Model: "m", RequestTimeoutS: 5}, []string{"m"})
	if proposed["context_source"] != "unverified" {
		t.Fatalf("an unpublished window was not reported unverified: %v", proposed)
	}
	if _, present := proposed["n_ctx"]; present {
		t.Fatalf("a window was proposed anyway: %v", proposed)
	}
}

// (a3). A server offering exactly one model has it selected; several are left to
// the picker, which keeps the previous choice.
func TestOneModelIsSelectedAndSeveralAreNot2l1(t *testing.T) {
	if got := onlyModel([]string{"only-one"}); got != "only-one" {
		t.Fatalf("a single model was not selected: %q", got)
	}
	if got := onlyModel([]string{"a", "b"}); got != "" {
		t.Fatalf("a choice was made for a multi-model server: %q", got)
	}
	if got := onlyModel(nil); got != "" {
		t.Fatalf("a model was invented for a server that listed none: %q", got)
	}
}

// (d) and the HOMEPC case, reproduced from W0's live finding: Ollama on the
// operator's tailnet answers /v1/models with HTTP 200 and
// `{"object":"list","data":null}`, because no model is pulled there. The refusal
// must say that, not accuse his model of not being served.
func TestAServerThatListsNoModelsSaysSo2l1(t *testing.T) {
	message := modelRefusalMessage("", "http://100.74.154.111:11434", nil)
	for _, want := range []string{"model is empty", "listed no models at all", "Pull or load a model"} {
		if !strings.Contains(message, want) {
			t.Fatalf("the empty-list refusal does not say %q: %s", want, message)
		}
	}
	named := modelRefusalMessage("model", "http://100.74.154.111:11434", nil)
	if !strings.Contains(named, "listed no models at all") {
		t.Fatalf("a named model against an empty list still blames the model: %s", named)
	}
	chosen := modelRefusalMessage("", "http://server", []string{"a", "b", "c"})
	for _, want := range []string{"model is empty", "3 models"} {
		if !strings.Contains(chosen, want) {
			t.Fatalf("the choose-one refusal does not say %q: %s", want, chosen)
		}
	}
}
