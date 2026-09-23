package tools

import (
	"context"
	"fmt"

	"harness/internal/session"
)

// Item 13 (v1.2.5): one search tool with a target, replacing search_text and
// find_files.
//
// The two had overlapping descriptions - each had to say "unlike the other
// one" - and the model sometimes picked the wrong one. They also cost 190
// tokens of schema on every request for what is one question asked of two
// different things: the contents of files, or their names.
//
// Nothing about either search changes. This dispatches on `target` and the two
// implementations behind it are the ones that were already there, jail, byte
// windows, result shapes and all.
type Search struct {
	content Tool
	names   Tool
}

// The two it dispatches to are taken as they are already built and wrapped, so
// the file-identity wrapper and everything else around them still applies.
func NewSearch(content, names Tool) *Search {
	return &Search{content: content, names: names}
}

func (*Search) Name() string { return "search" }

// Under 60 tokens, and it no longer has to distinguish itself from a sibling.
func (*Search) Description() string {
	return "Search local files under path: target=content returns matching text lines, target=name returns files whose names match pattern."
}

func (*Search) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"pattern": map[string]any{"type": "string"},
		"path":    map[string]any{"type": "string", "default": "."},
		"target":  map[string]any{"type": "string", "enum": []string{"content", "name"}, "default": "content"},
		"glob":    map[string]any{"type": "string", "description": "with target=content only: limit to filenames matching this glob"},
	}, "required": []string{"pattern"}}
}

// searchTarget reads the target, defaulting to content - the question asked far
// more often, and the one the pair's default behaviour already favoured.
func searchTarget(args map[string]any) string {
	if value, _ := args["target"].(string); value == "name" {
		return "name"
	}
	return "content"
}

func (s *Search) Call(ctx context.Context, item *session.Session, args map[string]any) (string, error) {
	return s.pick(args).Call(ctx, item, args)
}

// CallDetailed passes through when the tool behind the target has one, so a
// narrowly scoped operator retry is still offered exactly as it was.
func (s *Search) CallDetailed(ctx context.Context, item *session.Session, args map[string]any) CallDetail {
	tool := s.pick(args)
	if detailed, ok := tool.(DetailedTool); ok {
		return detailed.CallDetailed(ctx, item, args)
	}
	content, err := tool.Call(ctx, item, args)
	return CallDetail{Content: content, Err: err}
}

func (s *Search) CallAsOperator(ctx context.Context, item *session.Session, args map[string]any) (string, error) {
	tool := s.pick(args)
	override, ok := tool.(OperatorOverrideTool)
	if !ok {
		return "", fmt.Errorf("search target has no operator-identity override")
	}
	return override.CallAsOperator(ctx, item, args)
}

func (s *Search) pick(args map[string]any) Tool {
	if searchTarget(args) == "name" {
		return s.names
	}
	return s.content
}

// searchAliases are the two names this tool replaced. They are accepted for one
// release so a model that learned them, or a saved plan that names them, is not
// broken by the merge; the result says the name is going away. The registry
// itself counts twelve after web_search - an alias is not a tool.
var searchAliases = map[string]string{
	"search_text": "content",
	"find_files":  "name",
}

const searchAliasNotice = "note: %s is now `search` with target=%s and will be removed after this release.\n"
