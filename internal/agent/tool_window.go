package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

const (
	savedToolResultDir          = ".agentb-tool-results"
	savedToolResultSegmentBytes = 64 << 20
)

// Leave room for the assistant tool-call message and request framing that are
// not part of the pre-turn budget. An oversized byte window is retried rather
// than appended and allowed to make the next model turn impossible.
const toolResultContextMargin = 1024

func resultAllowance(budget events.Budget, remaining int) int {
	if budget.NCtx <= 0 || budget.NCtx > 32768 || budget.Ceiling <= 0 {
		return remaining
	}
	limit := max(0, budget.Ceiling-fixedContextTokens(budget)) / 2
	if remaining < 0 {
		return limit
	}
	return min(remaining, limit)
}

func (r *Runner) fitWindowResult(
	ctx context.Context,
	s *session.Session,
	connection *config.Connection,
	name string,
	args map[string]any,
	content string,
	ok bool,
	metadata map[string]any,
	resultTokens int,
	availableTokens int,
	operatorContext bool,
) (string, bool, map[string]any, int) {
	if availableTokens < 0 || resultTokens <= availableTokens {
		return content, ok, metadata, resultTokens
	}
	path, offset, saveErr := r.saveToolResult(s, content)
	note := fmt.Sprintf("[tool result cut: %d bytes; full output saved at %s; read_file path=%s offset=%d to page the omitted middle]", len(content), path, path, offset)
	if saveErr != nil {
		note = fmt.Sprintf("[tool result cut: %d bytes; saving the full output failed: %v]", len(content), saveErr)
	}
	cut := cutResultToTokens(content, note, availableTokens, func(value string) int { return r.textTokens(ctx, connection, value) })
	bounded := cloneMetadata(metadata)
	bounded["result_too_large"] = true
	bounded["original_result_tokens"] = resultTokens
	bounded["result_token_limit"] = availableTokens
	if saveErr == nil {
		bounded["saved_path"], bounded["saved_offset"], bounded["saved_bytes"] = path, offset, len(content)
	}
	return cut, ok, bounded, r.textTokens(ctx, connection, cut)
}

func (r *Runner) saveToolResult(s *session.Session, content string) (string, int, error) {
	if s == nil || s.Workspace == "" {
		return "", 0, fmt.Errorf("chat scratch is unavailable")
	}
	dir := filepath.Join(s.Workspace, savedToolResultDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	paths := []string{filepath.Join(dir, "results-1.txt"), filepath.Join(dir, "results-2.txt")}
	active := 0
	left, leftErr := os.Stat(paths[0])
	right, rightErr := os.Stat(paths[1])
	if rightErr == nil && (leftErr != nil || right.ModTime().After(left.ModTime())) {
		active = 1
	}
	size := int64(0)
	if info, err := os.Stat(paths[active]); err == nil {
		size = info.Size()
	}
	flags := os.O_CREATE | os.O_APPEND | os.O_WRONLY
	if size > 0 && size+int64(len(content)) > savedToolResultSegmentBytes {
		active, size, flags = 1-active, 0, os.O_CREATE|os.O_TRUNC|os.O_WRONLY
	}
	file, err := os.OpenFile(paths[active], flags, 0o600)
	if err != nil {
		return "", 0, err
	}
	_, writeErr := file.WriteString(content)
	closeErr := file.Close()
	if writeErr != nil {
		return "", 0, writeErr
	}
	if closeErr != nil {
		return "", 0, closeErr
	}
	relative, err := filepath.Rel(s.Workspace, paths[active])
	return filepath.ToSlash(relative), int(size) + 1, err
}

func savedToolResultRead(args map[string]any) bool {
	path, _ := args["path"].(string)
	path = strings.ToLower(filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))))
	return path == savedToolResultDir || strings.HasPrefix(path, savedToolResultDir+"/")
}

func cutResultToTokens(content, note string, limit int, count func(string) int) string {
	build := func(keep int) string {
		head := strings.ToValidUTF8(content[:keep*2/3], "")
		tail := strings.ToValidUTF8(content[len(content)-(keep-keep*2/3):], "")
		return head + "\n\n" + note + "\n\n" + tail
	}
	low, high, best := 0, len(content), note
	for low <= high {
		keep := low + (high-low)/2
		candidate := build(keep)
		if count(candidate) <= limit {
			best, low = candidate, keep+1
		} else {
			high = keep - 1
		}
	}
	return best
}

func cloneMetadata(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source)+5)
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
