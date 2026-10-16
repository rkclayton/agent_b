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
// server with exact accounting at acme's n_ctx: after the chat's history, the
// model reads long-input.txt in 16 KB windows. When another window cannot fit,
// the result says so but the next request still carries the complete toolset.
func TestALongReadKeepsItsToolsAfterTwoCutWindows2vf(t *testing.T) {
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
		if len(choices) >= 6 {
			return map[string]any{"content": "TOOLS STILL AVAILABLE"}
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
	// Item 2q5 (b) masks every older window, so the notes are larger than they were.
	request := "Read long-input.txt completely, window by window, until you reach the end, and tell me how many lines it has.\n\nMy notes so far:\n" + strings.Repeat("an earlier finding pasted in for this task. ", 1400)
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
	if cut != 0 {
		t.Fatalf("cut=%d; the harness must not end the read", cut)
	}
	for index, choice := range choices {
		if choice == "none" {
			t.Fatalf("request %d lost its tools", index+1)
		}
	}
	messages := item.MessagesCopy()
	if answer := messages[len(messages)-1]; answer.Role != "assistant" || answer.Content != "TOOLS STILL AVAILABLE" {
		t.Fatalf("the run's last message = %+v", answer)
	}
}
