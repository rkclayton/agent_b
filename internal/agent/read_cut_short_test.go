package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"harness/internal/events"
)

// Item 2fv, the second walk's 3-long (v0.70.1/W6, run r5) on the fake llama.cpp
// server with exact accounting at HomePC's n_ctx: after the chat's history, the
// model reads long-input.txt in 16 KB windows. Each window is under a quarter
// of the ceiling, so the running turn's newest four stay protected and nothing
// the elide may touch frees enough; windows stop fitting. The first refusal is
// returned as before; the second in a row ends the read, and the model —
// offered no tools from then on — answers from what it read with the
// cut-short line. The run ends done, not tool_errors.
func TestALongReadEndsInAnAnswerWhenTheWindowFills(t *testing.T) {
	nextOffset := regexp.MustCompile(`next_offset=(\d+)`)
	choices := []any{}
	server := newTemplateServer(t, func(body map[string]any, messages []map[string]any) map[string]any {
		if isSummaryRequest(messages) {
			return map[string]any{"content": "INTENT: read long-input.txt to its end\nFILES: long-input.txt, windows so far\nNEXT STEP: continue from the next window"}
		}
		choices = append(choices, body["tool_choice"])
		if body["tool_choice"] == "none" {
			return map[string]any{"content": "The read was cut short partway through long-input.txt, after the windows above; every line read so far is numbered in order."}
		}
		if text, _ := messages[len(messages)-1]["content"].(string); strings.Contains(text, "more=false") {
			return map[string]any{"content": "Read to the end: 3000 lines."}
		}
		offset := 1
		for index := len(messages) - 1; index >= 0; index-- {
			if messages[index]["role"] != "tool" {
				continue
			}
			text, _ := messages[index]["content"].(string)
			if match := nextOffset.FindStringSubmatch(text); match != nil {
				fmt.Sscanf(match[1], "%d", &offset)
				break
			}
		}
		return toolCall(fmt.Sprintf("read-%d-%d", offset, len(messages)), "read_file", fmt.Sprintf(`{"path":"long-input.txt","offset":%d,"limit":16384}`, offset))
	})
	runner, item, bus := templateRunner(t, server)
	lines := make([]string, 0, 3000)
	for index := 0; index < 3000; index++ {
		lines = append(lines, fmt.Sprintf("line %04d %s", index+1, strings.Repeat("lorem ipsum ", 9)))
	}
	if err := os.WriteFile(filepath.Join(item.Workspace, "long-input.txt"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	// The walk's summaries were rejected ("did not reduce session context"), so
	// the reads themselves filled the window. Here the pressure is the running
	// turn's own request — notes the operator pasted with it — which no
	// compaction may touch, so the protected reads fill the rest the same way.
	request := "Read long-input.txt completely, window by window, until you reach the end, and tell me how many lines it has.\n\nMy notes so far:\n" + strings.Repeat("an earlier finding pasted in for this task. ", 1100)
	if _, err := runner.AddUser(context.Background(), item, request); err != nil {
		t.Fatal(err)
	}
	reason, detail, turns := runner.Run(context.Background(), item, "r5")
	refused, cut := 0, 0
	for _, event := range bus.Recent(item.ID) {
		if event.Type != events.ToolResult {
			continue
		}
		data := event.Data.(map[string]any)
		preview, _ := data["preview"].(string)
		if strings.Contains(preview, "window too large") {
			refused++
		}
		if strings.HasPrefix(preview, "note: the read was cut short") {
			cut++
			if ok, _ := data["ok"].(bool); !ok {
				t.Fatalf("the cut-short result is an error: %v", data)
			}
		}
	}
	t.Logf("3-long (second walk): reason=%s turns=%d refused=%d cut=%d", reason, turns, refused, cut)
	if reason != "done" {
		t.Fatalf("run ended %s (%s) after %d turns; refused %d, cut %d", reason, detail, turns, refused, cut)
	}
	if refused < 1 || cut != 1 {
		t.Fatalf("refused=%d cut=%d; want a refusal before exactly one cut-short", refused, cut)
	}
	if last := choices[len(choices)-1]; last != "none" {
		t.Fatalf("the answering request's tool_choice = %v, want none", last)
	}
	messages := item.MessagesCopy()
	if answer := messages[len(messages)-1]; answer.Role != "assistant" || !strings.Contains(answer.Content, "cut short") {
		t.Fatalf("the run's last message = %+v", answer)
	}
	// The next message runs with tools again.
	if _, err := runner.AddUser(context.Background(), item, "Thanks."); err != nil {
		t.Fatal(err)
	}
	start := len(choices)
	runner.Run(context.Background(), item, "r6")
	if next := choices[start]; next == "none" {
		t.Fatal("the next run was still denied tools")
	}
}
