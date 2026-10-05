package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

func TestOperationalErrorIsPublished(t *testing.T) {
	bus := newCapturedBus()
	runner := &Runner{bus: bus.Bus}
	runner.operationalError(&session.Session{ID: "main"}, "run", "budget", fmt.Errorf("tokenizer unavailable"))
	recent := bus.Recent("main")
	if len(recent) != 1 || recent[0].Type != events.Error {
		t.Fatalf("recent events=%#v", recent)
	}
	data := recent[0].Data.(map[string]any)
	if data["where"] != "budget" || data["message"] != "tokenizer unavailable" {
		t.Fatalf("error data=%#v", data)
	}
}

func TestFallbackTokenCountAndPreviewUseUnicodeCharacters(t *testing.T) {
	runner := &Runner{}
	count, estimated := runner.count(context.Background(), &config.Connection{}, "🙂🙂🙂🙂")
	if count != 2 || !estimated {
		t.Fatalf("fallback count=%d estimated=%v", count, estimated)
	}
	value := strings.Repeat("🙂", 501)
	got := preview(value)
	if !utf8.ValidString(got) || len([]rune(got)) != 501 || !strings.HasSuffix(got, "…") {
		t.Fatalf("preview is not a 500-character UTF-8 window: runes=%d valid=%v", len([]rune(got)), utf8.ValidString(got))
	}
}

func TestS56ToolFailuresAreNamedForWhatFailed2qg(t *testing.T) {
	rows := []struct {
		content string
		want    string
	}{
		{"error: command failed\nexit=1\nSet-Location: path missing", "exit_nonzero"},
		{"error: command failed\nexit=1\nFAIL package", "exit_nonzero"},
		{"error: command failed\nexit=1\ncompile error", "exit_nonzero"},
		{"error: command failed\nexit=1", "exit_nonzero"},
		{"error: command failed\nexit=1\nParserError: unexpected token", "exit_nonzero"},
		{"error: note too long (max 300 Unicode characters)", "invalid_args"},
		{"error: note too long (max 300 Unicode characters)", "invalid_args"},
		{"error: file does not exist", "not_found"},
	}
	counts := map[string]int{}
	for _, row := range rows {
		got := ToolErrorClass(row.content)
		counts[got]++
		if got != row.want {
			t.Errorf("ToolErrorClass(%q) = %q, want %q", row.content, got, row.want)
		}
	}
	if counts["exit_nonzero"] != 5 || counts["invalid_args"] != 2 || counts["not_found"] != 1 || counts["internal"] != 0 {
		t.Fatalf("s56 classes=%v", counts)
	}
	if got := ToolErrorClass("error: fork/exec powershell.exe: invalid handle"); got != "internal" {
		t.Fatalf("harness process-start fault = %q, want internal", got)
	}
}
