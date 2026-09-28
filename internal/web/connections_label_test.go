package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
)

// Item 2mh (a): TEST NAMES THE CONNECTION WHEN THE OPERATOR HAS NOT.
//
// "model names — if the user hasnt entered one when setting up a connection we
// should set one during test phase based on model name." A connection is born
// labelled with its own generated id, and `server-2` identifies nothing.
func TestTestProposesALabelFromTheDiscoveredModel2mh(t *testing.T) {
	for _, one := range []struct {
		name   string
		tested config.Connection
		models []string
		want   string
	}{
		{"an untouched label takes the model's name", config.Connection{ID: "server-2", Label: "server-2", Model: "qwen3-coder-30b"}, nil, "qwen3-coder-30b"},
		{"an empty label takes it too", config.Connection{ID: "server-2", Model: "qwen3-coder-30b"}, nil, "qwen3-coder-30b"},
		{"a GGUF path is reduced to its basename, as the switcher reduces it", config.Connection{ID: "server-2", Label: "server-2", Model: `C:\models\Qwen3-Coder-30B-Q4_K_M.gguf`}, nil, "Qwen3-Coder-30B-Q4_K_M"},
		{"a label the operator chose is never replaced", config.Connection{ID: "server-2", Label: "the big one", Model: "qwen3-coder-30b"}, nil, ""},
		{"the placeholder is not a model", config.Connection{ID: "server-2", Label: "server-2", Model: "model"}, nil, ""},
		{"with no model of its own, the one the server offers", config.Connection{ID: "server-2", Label: "server-2"}, []string{"only-model"}, "only-model"},
		{"and nothing at all when the server offers several", config.Connection{ID: "server-2", Label: "server-2"}, []string{"one", "two"}, ""},
	} {
		t.Run(one.name, func(t *testing.T) {
			if got := proposedLabelFor(&one.tested, one.models); got != one.want {
				t.Fatalf("proposed label %q, want %q", got, one.want)
			}
		})
	}
	// And the browser applies it through the same proposal path every other learned
	// value travels on, under the same untouched rule.
	source, err := os.ReadFile(filepath.Join("..", "..", "web", "js", "settings.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "proposed.label") || !strings.Contains(string(source), "labelUntouched") {
		t.Fatal("the browser does not apply the proposed label")
	}
}
