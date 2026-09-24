package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/projection"
	"harness/internal/session"
)

func TestBudgetAccountsFetchedResultsSeparately(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	connection := cfg.Connections[0]
	item := &session.Session{ID: "fetch-budget", SchemaTokens: map[string]int{}}
	message := llm.Message{Role: "tool", Content: "untrusted fetched text"}
	record := events.Message{ID: "m-fetch", Role: "tool", Content: "untrusted fetched text", Category: "fetched"}
	budget, err := NewBudgeter().Measure(context.Background(), &connection, item, config.GlobalContext{Accounting: "estimated"}, budgetInput{SystemBase: "system", System: "system", Messages: []llm.Message{message}, Records: []events.Message{record}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if budget.Categories["fetched"] == 0 {
		t.Fatalf("fetched category was not charged: %+v", budget.Categories)
	}
	if budget.Categories["results"] != 0 {
		t.Fatalf("fetch leaked into results: %+v", budget.Categories)
	}
}

func TestColdPrefillSuppressesImmediateCompaction(t *testing.T) {
	budget := NewBudgeter()
	budget.RecordUsage("session", 20000, 1661)
	if !budget.ColdPrefill("session") {
		t.Fatal("cold prefill was not recognized")
	}
	budget.RecordUsage("session", 20000, 18000)
	if budget.ColdPrefill("session") {
		t.Fatal("warm prefix was classified cold")
	}
}

func TestExactAccountingUsesConnectionTimeoutAfterConnect(t *testing.T) {
	var slow sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apply-template":
			slow.Do(func() { time.Sleep(2700 * time.Millisecond) })
			_, _ = w.Write([]byte(`{"prompt":"rendered"}`))
		case "/tokenize":
			_, _ = w.Write([]byte(`{"tokens":[1]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	connection := config.Connection{BaseURL: server.URL, RequestTimeoutS: 5, Capabilities: config.Capabilities{Tokenize: true, ApplyTemplate: true, ApplyTemplateTools: true}}
	item := &session.Session{ID: "normal-timeout", SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	started := time.Now()
	if _, err := NewBudgeter().Measure(context.Background(), &connection, item, config.GlobalContext{}, budgetInput{SystemBase: "system", System: "system"}, false); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 2500*time.Millisecond {
		t.Fatalf("accounting returned before the old fast-fail boundary: %s", elapsed)
	}
}

func TestSlowTokenizeDegradesForOneMeasurementThenReturnsToExact(t *testing.T) {
	var slow atomic.Bool
	slow.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apply-template":
			_, _ = w.Write([]byte(`{"prompt":"rendered"}`))
		case "/tokenize":
			if slow.CompareAndSwap(true, false) {
				time.Sleep(1200 * time.Millisecond)
			}
			_, _ = w.Write([]byte(`{"tokens":[1]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	connection := config.Connection{BaseURL: server.URL, RequestTimeoutS: 1, Capabilities: config.Capabilities{Tokenize: true, ApplyTemplate: true, ApplyTemplateTools: true}}
	item := &session.Session{ID: "degraded", SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	input := budgetInput{SystemBase: "system", System: "system"}
	budgeter := NewBudgeter()
	degraded, err := budgeter.MeasureWithBusy(context.Background(), &connection, item, config.GlobalContext{}, input, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if degraded.Mode != "estimated" || !degraded.Estimated {
		t.Fatalf("degraded budget=%+v", degraded)
	}
	exact, err := budgeter.Measure(context.Background(), &connection, item, config.GlobalContext{}, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if exact.Mode != "exact" || exact.Estimated {
		t.Fatalf("recovered budget=%+v", exact)
	}
}

func TestRejectedApplyTemplateShapeUsesSentinelAndStaysExact(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apply-template":
			var body struct {
				Messages []llm.Message `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body.Messages) == 1 && body.Messages[0].Role == "system" && fail.CompareAndSwap(true, false) {
				http.Error(w, "No user query found in messages.", http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"prompt":"rendered"}`)
		case "/tokenize":
			fmt.Fprint(w, `{"tokens":[1]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	connection := config.Connection{BaseURL: server.URL, RequestTimeoutS: 5, Capabilities: config.Capabilities{Tokenize: true, ApplyTemplate: true}}
	item := &session.Session{ID: "template-retry", SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	budgeter := NewBudgeter()
	input := budgetInput{SystemBase: "system", System: "system"}
	repaired, err := budgeter.Measure(context.Background(), &connection, item, config.GlobalContext{}, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Mode != "exact" || len(repaired.Findings) != 0 {
		t.Fatalf("repaired budget=%+v", repaired)
	}
	retried, err := budgeter.Measure(context.Background(), &connection, item, config.GlobalContext{}, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Mode != "exact" || len(retried.Findings) != 0 {
		t.Fatalf("retried budget=%+v", retried)
	}
}

func TestEveryApplyTemplateFailureLeavesAnEstimatedBudget(t *testing.T) {
	input := budgetInput{
		SystemBase: "base", SystemProject: "project", SystemWorkspaceMemory: "workspace", System: "memory",
		Schemas: []any{testSchema("active")}, AllSchemas: map[string]any{"active": testSchema("active")},
		WithoutToolSystems: map[string]string{"active": "without active"},
		Messages:           []llm.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "answer"}},
		Records:            []events.Message{{ID: "user", Role: "user", Category: "history", Content: "hello"}, {ID: "assistant", Role: "assistant", Category: "history", Content: "answer"}},
	}
	run := func(failAt int) (events.Budget, int) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/apply-template":
				calls++
				if calls == failAt {
					http.Error(w, "injected apply-template failure", http.StatusInternalServerError)
					return
				}
				fmt.Fprint(w, `{"prompt":"rendered"}`)
			case "/tokenize":
				fmt.Fprint(w, `{"tokens":[1]}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		connection := config.Connection{BaseURL: server.URL, RequestTimeoutS: 5, Capabilities: config.Capabilities{Tokenize: true, ApplyTemplate: true, ApplyTemplateTools: true}}
		item := &session.Session{ID: fmt.Sprintf("failure-%d", failAt), SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
		budget, err := NewBudgeter().Measure(context.Background(), &connection, item, config.GlobalContext{}, input, false)
		if err != nil {
			t.Fatalf("failAt=%d err=%v", failAt, err)
		}
		return budget, calls
	}
	baseline, callCount := run(0)
	if baseline.Mode != "exact" || callCount < 2 {
		t.Fatalf("baseline=%+v calls=%d", baseline, callCount)
	}
	for failAt := 1; failAt <= callCount; failAt++ {
		budget, _ := run(failAt)
		if budget.Mode != "estimated" || !budget.Estimated || len(budget.Findings) != 1 || !strings.Contains(budget.Findings[0], "injected apply-template failure") {
			t.Fatalf("failAt=%d budget=%+v", failAt, budget)
		}
	}
}

func TestSystemOnlyAbortPrefixDegradesInsteadOfRaisingTemplateError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apply-template":
			var body struct {
				Messages []llm.Message `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(body.Messages)
			if strings.Contains(string(encoded), "HARNESS ABORT RECORD") {
				http.Error(w, "No user query found in messages.", http.StatusInternalServerError)
				return
			}
			for _, message := range body.Messages {
				if message.Role == "user" {
					fmt.Fprint(w, `{"prompt":"rendered"}`)
					return
				}
			}
			http.Error(w, "No user query found in messages.", http.StatusInternalServerError)
		case "/tokenize":
			fmt.Fprint(w, `{"tokens":[1]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	connection := config.Connection{BaseURL: server.URL, RequestTimeoutS: 5, Capabilities: config.Capabilities{Tokenize: true, ApplyTemplate: true}}
	item := &session.Session{ID: "abort-prefix", SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	input := budgetInput{
		SystemBase: "system", SystemProject: "system", SystemWorkspaceMemory: "system", System: "system",
		Messages: []llm.Message{{Role: "system", Content: "[HARNESS ABORT RECORD]"}},
		Records:  []events.Message{{ID: "m-abort", Role: "system", Category: "history", Content: "[HARNESS ABORT RECORD]"}},
	}
	budget, err := NewBudgeter().Measure(context.Background(), &connection, item, config.GlobalContext{}, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if budget.Mode != "estimated" || !budget.Estimated || len(budget.Findings) != 1 || !strings.Contains(budget.Findings[0], "No user query found") {
		t.Fatalf("abort budget=%+v", budget)
	}
}

func TestSystemAccountingUsesSentinelAndSubtractsItsCost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apply-template":
			var body struct {
				Messages []llm.Message `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if len(body.Messages) == 1 && body.Messages[0].Role == "system" {
				http.Error(w, "No user query found in messages.", http.StatusInternalServerError)
				return
			}
			prompt := ""
			for _, message := range body.Messages {
				prompt += messageText(message.Content)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"prompt": prompt})
		case "/tokenize":
			var body struct {
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": make([]int, len(body.Content))})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	connection := config.Connection{BaseURL: server.URL, RequestTimeoutS: 5, Capabilities: config.Capabilities{Tokenize: true, ApplyTemplate: true}}
	item := &session.Session{ID: "sentinel", SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	budget, err := NewBudgeter().Measure(context.Background(), &connection, item, config.GlobalContext{}, budgetInput{SystemBase: "direct system", System: "direct system"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := budget.Categories["system"], len("direct system"); got < want-1 || got > want+1 {
		t.Fatalf("sentinel-differenced system count=%d, direct count=%d", got, want)
	}
}

func TestExactSchemaAttributionTokenizerFailureDegrades(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apply-template":
			var body struct {
				Tools []map[string]any `json:"tools"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if len(body.Tools) == 1 && schemaName(body.Tools[0]) == "read_file" {
				fmt.Fprint(w, `{"prompt":"fail-schema"}`)
				return
			}
			fmt.Fprint(w, `{"prompt":"rendered"}`)
		case "/tokenize":
			var body struct {
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if body.Content == "fail-schema" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"tokens":[1]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	connection := config.Connection{BaseURL: server.URL, RequestTimeoutS: 5, Capabilities: config.Capabilities{Tokenize: true, ApplyTemplate: true, ApplyTemplateTools: true}}
	item := &session.Session{ID: "exact", SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	readFile := testSchema("read_file")
	budget, err := NewBudgeter().Measure(context.Background(), &connection, item, config.GlobalContext{}, budgetInput{SystemBase: "system", System: "system", Schemas: []any{testSchema("active")}, AllSchemas: map[string]any{"read_file": readFile}}, false)
	if err != nil || budget.Mode != "estimated" || len(budget.Findings) != 1 || !strings.Contains(budget.Findings[0], "tokenize") {
		t.Fatalf("budget=%+v error=%v", budget, err)
	}
}

func TestExactToolCostsAreMarginalAndCached(t *testing.T) {
	var templateCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apply-template":
			templateCalls.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			messages := body["messages"].([]any)
			prompt := ""
			for _, raw := range messages {
				prompt += raw.(map[string]any)["content"].(string)
			}
			if tools, present := body["tools"]; present {
				prompt += "|shared-tool-template|"
				for _, raw := range tools.([]any) {
					prompt += schemaName(raw.(map[string]any)) + "|schema|"
				}
			}
			data, _ := json.Marshal(map[string]string{"prompt": prompt})
			_, _ = w.Write(data)
		case "/tokenize":
			var body struct {
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			tokens := make([]int, len(body.Content))
			data, _ := json.Marshal(map[string]any{"tokens": tokens})
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connection := config.Connection{BaseURL: server.URL, RequestTimeoutS: 5, Capabilities: config.Capabilities{Tokenize: true, ApplyTemplate: true, ApplyTemplateTools: true}}
	item := &session.Session{ID: "cache", SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	one, two := testSchema("one"), testSchema("two")
	input := budgetInput{
		SystemBase:         "system",
		System:             "system tools one, two",
		WithoutToolSystems: map[string]string{"one": "system tools two", "two": "system tools one"},
		Schemas:            []any{one, two},
		AllSchemas:         map[string]any{"one": one, "two": two},
	}
	budgeter := NewBudgeter()
	first, err := budgeter.Measure(context.Background(), &connection, item, config.GlobalContext{}, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := templateCalls.Load(); got != 9 {
		t.Fatalf("first template renders=%d, want 9 (1 sentinel + 4 budget + 2 isolated + 2 marginal)", got)
	}
	if first.ToolMarginalTokens["one"] <= 0 || first.ToolMarginalTokens["two"] <= 0 {
		t.Fatalf("marginal costs=%v", first.ToolMarginalTokens)
	}
	if first.ToolSchemaTokens["one"] <= first.ToolMarginalTokens["one"] {
		t.Fatalf("isolated schema should include more shared overhead: schema=%v marginal=%v", first.ToolSchemaTokens, first.ToolMarginalTokens)
	}
	budgeter.MarkRequest(item.ID, first.UsedEst)
	budgeter.RecordUsage(item.ID, first.UsedEst+5, -1)
	second, err := budgeter.Measure(context.Background(), &connection, item, config.GlobalContext{}, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := templateCalls.Load(); got != 13 {
		t.Fatalf("cached template renders=%d, want 13 (four regular renders added)", got)
	}
	if fmt.Sprint(second.ToolMarginalTokens) != fmt.Sprint(first.ToolMarginalTokens) {
		t.Fatalf("cached marginals changed: first=%v second=%v", first.ToolMarginalTokens, second.ToolMarginalTokens)
	}
	budgeter.InvalidateToolCosts()
	_, err = budgeter.Measure(context.Background(), &connection, item, config.GlobalContext{}, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := templateCalls.Load(); got != 21 {
		t.Fatalf("one prefix invalidation rendered costs again: calls=%d, want 21", got)
	}

	input.System = "system tools one"
	input.WithoutToolSystems = map[string]string{"one": "system tools "}
	input.Schemas = []any{one}
	third, err := budgeter.Measure(context.Background(), &connection, item, config.GlobalContext{}, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := templateCalls.Load(); got != 28 {
		t.Fatalf("tool-change template renders=%d, want 28 (4 budget + 2 isolated + 1 marginal added)", got)
	}
	if _, found := third.ToolMarginalTokens["two"]; found {
		t.Fatalf("disabled tool acquired a marginal request cost: %v", third.ToolMarginalTokens)
	}
}

func TestMessageWeightsCacheByIDAndInvalidateOnContentChange(t *testing.T) {
	var weightCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apply-template":
			var body struct {
				Messages []llm.Message `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			data, _ := json.Marshal(map[string]string{"prompt": strings.Repeat("p", len(body.Messages)*10)})
			_, _ = w.Write(data)
		case "/tokenize":
			var body struct {
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if strings.HasPrefix(body.Content, "weight-") || body.Content == "[elided]" {
				weightCalls.Add(1)
			}
			tokens := make([]int, max(1, len(body.Content)))
			data, _ := json.Marshal(map[string]any{"tokens": tokens})
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connection := config.Connection{BaseURL: server.URL, Model: "model", RequestTimeoutS: 5, Capabilities: config.Capabilities{Tokenize: true, ApplyTemplate: true}}
	item := &session.Session{ID: "message-cache", SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	input := budgetInput{SystemBase: "system", System: "system"}
	for index := 0; index < 19; index++ {
		content := fmt.Sprintf("weight-%02d", index)
		input.Messages = append(input.Messages, llm.Message{Role: "user", Content: content})
		input.Records = append(input.Records, events.Message{ID: fmt.Sprintf("m-%02d", index), Role: "user", Content: content, Category: "history"})
	}
	budgeter := NewBudgeter()
	measure := func() {
		t.Helper()
		if _, err := budgeter.Measure(context.Background(), &connection, item, config.GlobalContext{}, input, false); err != nil {
			t.Fatal(err)
		}
	}
	measure()
	if got := weightCalls.Load(); got != 19 {
		t.Fatalf("cold weight calls=%d, want 19", got)
	}
	measure()
	if got := weightCalls.Load(); got != 19 {
		t.Fatalf("warm weight calls=%d, want no new calls", got)
	}

	input.Messages = append(input.Messages, llm.Message{Role: "user", Content: "weight-new"})
	input.Records = append(input.Records, events.Message{ID: "m-new", Role: "user", Content: "weight-new", Category: "history"})
	measure()
	if got := weightCalls.Load(); got != 20 {
		t.Fatalf("appended-message weight calls=%d, want one new call", got)
	}

	input.Messages[0].Content, input.Records[0].Content = "weight-edited", "weight-edited"
	measure()
	if got := weightCalls.Load(); got != 21 {
		t.Fatalf("edited-message weight calls=%d, want one invalidated call", got)
	}
	input.Messages[1].Content, input.Records[1].Content = "[elided]", "[elided]"
	measure()
	if got := weightCalls.Load(); got != 22 {
		t.Fatalf("elided-message weight calls=%d, want one invalidated call", got)
	}
}

func TestOperatorR5TapeReplaysThroughResilientAccounting(t *testing.T) {
	path := os.Getenv("AGENTB_R5_TAPE")
	if path == "" {
		t.Skip("set AGENTB_R5_TAPE to the operator's main-20260908T134338 tape")
	}
	replay, err := projection.LoadReplay([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := replay.Sessions["main"]
	if !ok || len(snapshot.Messages) < 19 {
		t.Fatalf("replayed messages=%d main=%t", len(snapshot.Messages), ok)
	}
	var slow atomic.Bool
	slow.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apply-template":
			_, _ = w.Write([]byte(`{"prompt":"rendered"}`))
		case "/tokenize":
			if slow.CompareAndSwap(true, false) {
				time.Sleep(1200 * time.Millisecond)
			}
			_, _ = w.Write([]byte(`{"tokens":[1]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	connection := config.Connection{BaseURL: server.URL, Model: "r5-replay", RequestTimeoutS: 1, Context: config.Context{NCtx: 32768, ReserveOutput: 10240}, Capabilities: config.Capabilities{Tokenize: true, ApplyTemplate: true}}
	item := &session.Session{ID: "r5-replay", Messages: append([]events.Message(nil), snapshot.Messages...), SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
	messages := make([]llm.Message, 0, len(snapshot.Messages))
	for _, message := range snapshot.Messages {
		messages = append(messages, requestMessage(&connection, item, message))
	}
	input := budgetInput{SystemBase: "system", System: "system", Messages: messages, Records: snapshot.Messages}
	budgeter := NewBudgeter()
	degraded, err := budgeter.MeasureWithBusy(context.Background(), &connection, item, config.GlobalContext{}, input, false, nil)
	if err != nil || degraded.Mode != "estimated" {
		t.Fatalf("degraded=%+v err=%v", degraded, err)
	}
	exact, err := budgeter.Measure(context.Background(), &connection, item, config.GlobalContext{}, input, false)
	if err != nil || exact.Mode != "exact" {
		t.Fatalf("exact=%+v err=%v", exact, err)
	}
}

func testSchema(name string) map[string]any {
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": "test", "parameters": map[string]any{"type": "object"}}}
}

func schemaName(schema map[string]any) string {
	function, _ := schema["function"].(map[string]any)
	name, _ := function["name"].(string)
	return name
}
