package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
)

func runScaledCompaction2tv(t *testing.T, nctx, initialBytes, resultBytes, turns int) (int, []int, *templateServer) {
	t.Helper()
	previousNCtx := templateNCtx
	templateNCtx = nctx
	t.Cleanup(func() { templateNCtx = previousNCtx })
	step := 0
	server := newTemplateServer(t, func(body map[string]any, _ []map[string]any) map[string]any {
		if stream, _ := body["stream"].(bool); !stream {
			return map[string]any{"content": "DONE: read\nNEXT: continue\nFILES CHANGED: none\nOPEN QUESTIONS: none"}
		}
		step++
		if step <= turns {
			return toolCall(fmt.Sprintf("read-%d", step), "read_file", fmt.Sprintf(`{"path":"part.txt","offset":%d}`, step))
		}
		return map[string]any{"content": "done"}
	})
	server.tokenScale = 10
	runner, item, bus := templateRunnerReserve(t, server, nctx/20)
	if err := os.WriteFile(filepath.Join(item.Workspace, "part.txt"), []byte(strings.Repeat("x", resultBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	if initialBytes > 0 {
		item.Append(events.Message{ID: "old", Role: "assistant", Category: "history", Content: strings.Repeat("h", initialBytes)})
	}
	if _, err := runner.AddUser(context.Background(), item, "read repeatedly"); err != nil {
		t.Fatal(err)
	}
	if reason, detail, _ := runner.Run(context.Background(), item, "run"); reason != "done" {
		t.Fatalf("run=%s %q", reason, detail)
	}
	after := []int{}
	for _, event := range bus.Recent(item.ID) {
		if event.Type == events.Compaction {
			after = append(after, asInt(event.Data.(map[string]any)["after"]))
		}
	}
	return len(after), after, server
}

// Item 2tv: the soft line never mutates an already-sent request. Compaction has
// one configured line, summary_pct; cold-prefill state cannot move it earlier.
func TestCompactionDoesNothingAtTheSoftLine2tv(t *testing.T) {
	cfg := config.GlobalContext{SoftPct: .75, SummaryPct: .85}
	if shouldBatchElide(7400, 10000, cfg, false) {
		t.Fatal("elided below the soft line")
	}
	if shouldBatchElide(7500, 10000, cfg, false) {
		t.Fatal("elided at the soft line")
	}
	if shouldBatchElide(9000, 10000, cfg, true) {
		t.Fatal("cold prefill caused an early elision")
	}
}

func TestCompactionTriggerTravelsOnTheContext(t *testing.T) {
	if got := compactionTrigger(context.Background()); got != "" {
		t.Fatalf("unset trigger = %q", got)
	}
	if got := compactionTrigger(withCompactionTrigger(context.Background(), "summary_pct")); got != "summary_pct" {
		t.Fatalf("trigger = %q", got)
	}
}

func TestSummaryLineStubsFirstAndSummarizesOnlyWhenNeeded2tv(t *testing.T) {
	for _, conversationOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("conversation_only_%t", conversationOnly), func(t *testing.T) {
			server := newSummaryServer(t, "DONE: enough\nNEXT: continue\nFILES CHANGED: none\nOPEN QUESTIONS: none")
			runner, item, bus, _ := compactionRunner(t, server, nil, 32768)
			messages := []events.Message{}
			ok := true
			for index := 0; index < 7; index++ {
				if conversationOnly {
					messages = append(messages, events.Message{ID: fmt.Sprintf("answer-%d", index), Role: "assistant", Category: "history", Content: strings.Repeat("talk ", 2500), Tokens: 3500})
					continue
				}
				callID := fmt.Sprintf("read-%d", index)
				messages = append(messages,
					events.Message{ID: "call-" + callID, Role: "assistant", Category: "history", ToolCalls: []events.ToolCall{{ID: callID, Name: "read_file", Arguments: fmt.Sprintf(`{"path":"part-%d"}`, index)}}},
					events.Message{ID: "result-" + callID, Role: "tool", Name: "read_file", Category: "files", ToolCallID: callID, Content: strings.Repeat("data ", 2500), Tokens: 3500, OK: &ok})
			}
			messages = append(messages, events.Message{ID: "task", Role: "user", Category: "history", Content: "finish", Tokens: 2})
			item.ReplaceMessages(messages)
			item.SetRunPin("task")
			connection := connectionForRunner(runner, "main")
			if !runner.compactAfterTurn(context.Background(), item, "run", 8, connection, nil) {
				t.Fatal("summary-line pass made no change")
			}
			compactions, after := 0, 0
			for _, event := range bus.Recent(item.ID) {
				if event.Type == events.Compaction {
					compactions++
					after = asInt(event.Data.(map[string]any)["after"])
				}
			}
			wantCalls := int32(0)
			if conversationOnly {
				wantCalls = 1
			}
			if calls := server.chatCalls.Load(); calls != wantCalls || compactions != 1 || after > 24576/2 {
				t.Fatalf("summary calls=%d want=%d compactions=%d after=%d", calls, wantCalls, compactions, after)
			}
		})
	}
}

func TestRepeatedReadsStayByteStableBeforeTheSummaryLine2tv(t *testing.T) {
	compactions, _, server := runScaledCompaction2tv(t, 200000, 0, 100, 60)
	if compactions != 0 {
		t.Fatalf("compactions=%d before the line", compactions)
	}
	server.mu.Lock()
	requests := append([]string(nil), server.requests...)
	server.mu.Unlock()
	for index := 1; index < len(requests); index++ {
		var prior, next []map[string]any
		_ = json.Unmarshal([]byte(requests[index-1]), &prior)
		_ = json.Unmarshal([]byte(requests[index]), &next)
		if len(next) < len(prior) || !reflect.DeepEqual(prior, next[:len(prior)]) {
			at := 0
			for at < len(prior) && at < len(next) && reflect.DeepEqual(prior[at], next[at]) {
				at++
			}
			t.Fatalf("request %d changed message %d: before=%v after=%v", index+1, at, prior[min(at, len(prior)-1)], next[min(at, len(next)-1)])
		}
	}
}

func TestCompactionIsProportionalAndLeavesTenTurnsClear2tv(t *testing.T) {
	largeCount, largeAfter, _ := runScaledCompaction2tv(t, 200000, 40000, 600, 70)
	if largeCount != 1 || largeAfter[0] > 190000/2 {
		t.Fatalf("200k compactions=%d after=%v", largeCount, largeAfter)
	}
	smallCount, smallAfter, _ := runScaledCompaction2tv(t, 32000, 4000, 600, 70)
	if smallCount <= largeCount {
		t.Fatalf("32k compactions=%d, 200k=%d", smallCount, largeCount)
	}
	for _, after := range smallAfter {
		if after > 30400/2 {
			t.Fatalf("32k compaction landed at %d, want <= %d", after, 30400/2)
		}
	}
	t.Logf("2tv proportional counts: 32k=%d 200k=%d; 200k after=%v", smallCount, largeCount, largeAfter)
}
