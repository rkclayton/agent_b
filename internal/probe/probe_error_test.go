package probe

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/llm"
)

func TestProbeFailureNamesMalformedBaseURL(t *testing.T) {
	connection := config.Defaults(t.TempDir()).Connections[0]
	connection.BaseURL = "https:/host"
	_, _, err := Probe(context.Background(), &connection)
	if err == nil || !strings.Contains(err.Error(), `base_url "https:/host" is malformed`) || !strings.Contains(err.Error(), "two slashes") {
		t.Fatalf("error=%v", err)
	}
}

func TestEvalReadsTheNamedByteLimit2sv(t *testing.T) {
	for input, want := range map[string]int{
		`{"error":"prompt too large: 401628 bytes (limit 400000)"}`: 400000,
		`maximum request size is 524,288 bytes`:                     524288,
	} {
		if got := byteLimitFromResponse(input); got != want {
			t.Fatalf("%q: got %d want %d", input, got, want)
		}
	}
}

func TestEvalReplacesOrDropsALearnedByteLimit2sw(t *testing.T) {
	for _, row := range []struct {
		name, refusal string
		want          int
		finding       string
	}{
		{"tokens", `{"error":"prompt exceeds context window by 1 token"}`, 0, "size limit: none found"},
		{"new bytes", `{"error":"prompt too large: 501000 bytes (limit 500000)"}`, 500000, "size limit: 500000 bytes"},
	} {
		t.Run(row.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, row.refusal, http.StatusBadRequest) }))
			defer server.Close()
			connection := config.Defaults(t.TempDir()).Connections[0]
			connection.BaseURL, connection.Model = server.URL, "model"
			caps := config.Capabilities{Server: "llama.cpp", NCtx: 8, Props: true, ObservedByteLimit: 400000}
			findings := []string{"size limit: 400000 bytes observed 2026-10-01"}
			probeOverflow(context.Background(), llm.New(&connection), &connection, &caps, &findings, true)
			if caps.ObservedByteLimit != row.want || !strings.Contains(strings.Join(findings, "\n"), row.finding) {
				t.Fatalf("limit=%d findings=%v", caps.ObservedByteLimit, findings)
			}
		})
	}
}

func TestConnectionFailureNamesDNSName(t *testing.T) {
	err := connectionProbeErrorFor("https://nosuch.invalid", fmt.Errorf("props unavailable"), &net.DNSError{Name: "nosuch.invalid", Err: "no such host"})
	if got := err.Error(); got != `The name "nosuch.invalid" does not resolve.` {
		t.Fatalf("error=%q", got)
	}
}

func TestProbeFailureNamesCertificateReason(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	connection := config.Defaults(t.TempDir()).Connections[0]
	connection.BaseURL = server.URL
	_, _, err := Probe(context.Background(), &connection)
	if err == nil || !strings.Contains(err.Error(), "TLS failed:") || !strings.Contains(strings.ToLower(err.Error()), "certificate") {
		t.Fatalf("error=%v", err)
	}
}

func TestProbeFailureNamesHTTPStatusAndFirstLine(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, "endpoint says no\nsecond line")
			}))
			defer server.Close()
			connection := config.Defaults(t.TempDir()).Connections[0]
			connection.BaseURL = server.URL
			_, _, err := Probe(context.Background(), &connection)
			want := fmt.Sprintf("HTTP %d: endpoint says no", status)
			if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "second line") {
				t.Fatalf("error=%v, want %q only", err, want)
			}
		})
	}
}

func TestProbeFailureNamesUnservedModelAndListedModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"zeta"},{"id":"alpha"},{"id":"beta"},{"id":"gamma"},{"id":"delta"},{"id":"epsilon"}]}`)
	}))
	defer server.Close()
	connection := config.Defaults(t.TempDir()).Connections[0]
	connection.BaseURL, connection.Model = server.URL, "model"
	_, _, err := Probe(context.Background(), &connection)
	if err == nil || !strings.Contains(err.Error(), `Model "model" is not served; this server lists:`) {
		t.Fatalf("error=%v", err)
	}
	listed := strings.TrimPrefix(err.Error(), `Model "model" is not served; this server lists: `)
	if strings.Count(listed, ",") != 4 {
		t.Fatalf("expected at most five listed models, got %q", listed)
	}
}

// Item 2px (h) CHECK 9: a negative tool verdict carries its cause in one word.
func TestToolProbeSaysWhy2px(t *testing.T) {
	call := func(name, args string) llm.Response {
		return llm.Response{ToolCalls: []llm.ToolCall{{Function: llm.FunctionCall{Name: name, Arguments: args}}}}
	}
	for _, row := range []struct {
		response llm.Response
		err      error
		timedOut bool
		want     string
	}{
		{call("read_file", `{"path":"main.go"}`), nil, false, ""},
		{llm.Response{}, nil, false, "no call"},
		{call("write_file", `{}`), nil, false, "wrong tool"},
		{call("read_file", `{"path":`), nil, false, "bad arguments"},
		{llm.Response{}, context.DeadlineExceeded, true, "timed out"},
		{llm.Response{}, &llm.ResponseShapeError{Status: 400}, false, "refused"},
		{llm.Response{}, &llm.ResponseShapeError{Status: 500}, false, "server error"},
	} {
		if got := toolCallCause(row.response, row.err, row.timedOut); got != row.want {
			t.Errorf("cause = %q, want %q", got, row.want)
		}
	}
}

// Item 2px (h) CHECK 9, against fixtures: a model that thinks 600 tokens before it
// calls, one that loads for the cold-load allowance then calls, no model chosen, and
// one that never calls. The cold load is scaled: 300 ms against a 1 s allowance
// stands for 30 s against 120 s.
func TestToolProbeArms2px(t *testing.T) {
	defer func(previous time.Duration) { toolProbeTimeout = previous }(toolProbeTimeout)
	toolProbeTimeout = time.Second
	call := `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}}]},"finish_reason":"tool_calls"}]}`
	answer := `{"choices":[{"message":{"role":"assistant","content":"I will not."},"finish_reason":"stop"}]}`
	for _, arm := range []struct {
		name, model, want string
		reply             func(body []byte) string
	}{
		{"thinks 600 then calls", "m", "available", func(body []byte) string {
			if bytes.Contains(body, []byte(`"enable_thinking":true`)) {
				return answer // 512 tokens spent thinking, no call reached
			}
			return call
		}},
		{"cold load then calls", "m", "available", func([]byte) string { time.Sleep(300 * time.Millisecond); return call }},
		{"no model chosen", "", "choose a model to check tool calling", func([]byte) string { return call }},
		{"never calls", "m", "unavailable (no call)", func([]byte) string { return answer }},
	} {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			body, _ := io.ReadAll(r.Body)
			fmt.Fprint(w, arm.reply(body))
		}))
		connection := config.Defaults(t.TempDir()).Connections[0]
		connection.BaseURL, connection.Model, connection.Reasoning.Control, connection.Reasoning.Enabled = server.URL, arm.model, "chat_template_kwargs", true
		_, line := probeToolCalls(context.Background(), &connection)
		server.Close()
		t.Logf("2px tool probe %-22s -> %q, %d request(s)", arm.name, line, requests)
		if line != arm.want || (arm.model == "" && requests != 0) {
			t.Errorf("%s: %q after %d request(s), want %q", arm.name, line, requests, arm.want)
		}
	}
}
