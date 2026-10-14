package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"harness/internal/session"
)

type Code struct{ diagnostics *languageDiagnostics }
type codePosition struct{ Line, Character int }
type codeRange struct{ Start, End codePosition }
type codeLocation struct {
	URI   string
	Range codeRange
}
type codeSymbol struct {
	Name                  string
	Kind                  int
	Location              codeLocation
	Range, SelectionRange codeRange
}
type codeEntry struct {
	kind, name, path string
	line, column     int
}

func NewCode(diagnostics *languageDiagnostics) *Code { return &Code{diagnostics: diagnostics} }
func (*Code) Name() string                           { return "code" }
func (*Code) Description() string {
	return "Ask a language server where a named symbol is defined or referenced, what a file or folder holds, or which folder symbols match a word. Give a name; path and one-based line may narrow it. Results are bounded; use search_text when no server is installed."
}
func (*Code) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"definition", "references", "outline", "symbols"}}, "name": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"}, "line": map[string]any{"type": "integer", "minimum": 1}}, "required": []string{"action"}}
}
func (c *Code) Call(ctx context.Context, s *session.Session, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	name, _ := args["name"].(string)
	path, _ := args["path"].(string)
	pathGiven := path != ""
	line := number(args["line"], 0)
	if action != "outline" && strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("name is required")
	}
	if action != "definition" && action != "references" && action != "outline" && action != "symbols" {
		return "", fmt.Errorf("action must be definition, references, outline, or symbols")
	}
	root, err := session.RealPath(s.Workspace)
	if err != nil {
		return "", err
	}
	if path != "" {
		resolved, err := Resolve(root, path)
		if err != nil {
			return "", err
		}
		path = resolved
	} else {
		path = c.sourceFile(root)
	}
	if path == "" {
		return "code: no language server installed for this folder; use search_text", nil
	}
	if action == "outline" {
		return c.outline(ctx, root, path)
	}
	if action == "symbols" {
		return c.symbols(ctx, root, path, name)
	}
	candidates, status := c.workspaceSymbols(ctx, root, path, name)
	if result := codeStatus(status, filepath.Ext(path)); result != "" {
		return result, nil
	}
	filterPath := ""
	if pathGiven {
		filterPath = path
	}
	candidates = narrowCodeEntries(candidates, filterPath, line, name)
	if len(candidates) != 1 {
		return boundedCode(root, "candidates:", candidates), nil
	}
	selected := candidates[0]
	params := map[string]any{"textDocument": map[string]any{"uri": fileURI(selected.path)}, "position": map[string]any{"line": selected.line - 1, "character": selected.column}}
	method := "textDocument/definition"
	if action == "references" {
		method = "textDocument/references"
		params["context"] = map[string]any{"includeDeclaration": true}
	}
	raw, status := c.diagnostics.Query(ctx, root, selected.path, method, params)
	if result := codeStatus(status, filepath.Ext(path)); result != "" {
		return result, nil
	}
	var locations []codeLocation
	if len(raw) > 0 && raw[0] == '{' {
		var one codeLocation
		if json.Unmarshal(raw, &one) == nil {
			locations = []codeLocation{one}
		}
	} else {
		_ = json.Unmarshal(raw, &locations)
	}
	entries := c.locations(root, locations)
	return boundedCode(root, action+":", entries), nil
}
func (c *Code) sourceFile(root string) string {
	var files []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() && path != root && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !entry.IsDir() && len(c.diagnostics.commands[strings.ToLower(filepath.Ext(path))]) > 0 {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	if len(files) > 0 {
		return files[0]
	}
	return ""
}
func (c *Code) workspaceSymbols(ctx context.Context, root, path, query string) ([]codeEntry, lspQueryStatus) {
	raw, status := c.diagnostics.Query(ctx, root, path, "workspace/symbol", map[string]any{"query": query})
	if status != lspQueryOK {
		return nil, status
	}
	var symbols []codeSymbol
	_ = json.Unmarshal(raw, &symbols)
	entries := make([]codeEntry, 0, len(symbols))
	for _, symbol := range symbols {
		candidate := uriPath(symbol.Location.URI)
		if candidate == "" || !within(root, candidate) {
			continue
		}
		entries = append(entries, codeEntry{kind: codeKind(symbol.Kind), name: symbol.Name, path: candidate, line: symbol.Location.Range.Start.Line + 1, column: symbol.Location.Range.Start.Character})
	}
	return entries, status
}
func (c *Code) symbols(ctx context.Context, root, path, name string) (string, error) {
	entries, status := c.workspaceSymbols(ctx, root, path, name)
	if result := codeStatus(status, filepath.Ext(path)); result != "" {
		return result, nil
	}
	return boundedCode(root, "symbols:", entries), nil
}
func (c *Code) outline(ctx context.Context, root, path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	files := []string{path}
	if info.IsDir() {
		files = nil
		_ = filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil && !entry.IsDir() && len(c.diagnostics.commands[strings.ToLower(filepath.Ext(candidate))]) > 0 {
				files = append(files, candidate)
			}
			return nil
		})
		sort.Strings(files)
	}
	entries := []codeEntry{}
	for _, file := range files {
		raw, status := c.diagnostics.Query(ctx, root, file, "textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": fileURI(file)}})
		if result := codeStatus(status, filepath.Ext(file)); result != "" {
			return result, nil
		}
		var symbols []codeSymbol
		_ = json.Unmarshal(raw, &symbols)
		for _, symbol := range symbols {
			entries = append(entries, codeEntry{kind: codeKind(symbol.Kind), name: symbol.Name, path: file, line: symbol.Range.Start.Line + 1})
		}
	}
	return boundedCode(root, "outline:", entries), nil
}
func narrowCodeEntries(entries []codeEntry, path string, line int, name string) []codeEntry {
	out := entries[:0]
	for _, entry := range entries {
		if !strings.EqualFold(entry.name, name) {
			continue
		}
		if path != "" && !strings.EqualFold(filepath.Clean(entry.path), filepath.Clean(path)) {
			continue
		}
		if line > 0 && entry.line != line {
			continue
		}
		out = append(out, entry)
	}
	return out
}
func (c *Code) locations(root string, locations []codeLocation) []codeEntry {
	entries := make([]codeEntry, 0, len(locations))
	for _, location := range locations {
		path := uriPath(location.URI)
		if path == "" || !within(root, path) {
			continue
		}
		line := location.Range.Start.Line + 1
		text := ""
		if data, err := os.ReadFile(path); err == nil {
			lines := strings.Split(string(data), "\n")
			if line <= len(lines) {
				text = strings.TrimSpace(lines[line-1])
			}
		}
		entries = append(entries, codeEntry{path: path, line: line, name: text})
	}
	return entries
}
func boundedCode(root, heading string, entries []codeEntry) string {
	lines := []string{heading}
	omitted := max(0, len(entries)-50)
	for _, entry := range entries[:min(50, len(entries))] {
		path := entry.path
		if relative, err := filepath.Rel(root, path); err == nil {
			path = relative
		}
		line := ""
		if entry.kind != "" {
			line = entry.kind + " " + entry.name + " " + filepath.ToSlash(path) + fmt.Sprintf(":%d", entry.line)
		} else {
			line = filepath.ToSlash(path) + fmt.Sprintf(":%d", entry.line)
			if entry.name != "" {
				line += " " + entry.name
			}
		}
		candidate := strings.Join(append(lines, line), "\n") + fmt.Sprintf("\n%d more", len(entries))
		if estimateCodeTokens(candidate) > 2000 {
			omitted += min(50, len(entries)) - len(lines) + 1
			break
		}
		lines = append(lines, line)
	}
	if omitted > 0 {
		lines = append(lines, fmt.Sprintf("%d more", omitted))
	}
	return strings.Join(lines, "\n")
}
func estimateCodeTokens(text string) int { return int(math.Ceil(float64(len([]rune(text))) / 3.6)) }
func codeStatus(status lspQueryStatus, extension string) string {
	if status == lspQueryMissing {
		return fmt.Sprintf("code: no language server installed for %s; use search_text", strings.ToLower(extension))
	}
	if status == lspQueryTimeout {
		return "code: language server did not answer in 3 s"
	}
	return ""
}
func uriPath(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "file" {
		return ""
	}
	path := filepath.FromSlash(parsed.Path)
	if len(path) >= 3 && path[0] == filepath.Separator && path[2] == ':' {
		path = path[1:]
	}
	return filepath.Clean(path)
}
func codeKind(kind int) string {
	if value := map[int]string{2: "module", 5: "class", 6: "method", 12: "function", 13: "variable", 14: "constant", 23: "struct"}[kind]; value != "" {
		return value
	}
	return "symbol"
}
