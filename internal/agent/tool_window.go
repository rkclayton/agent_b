package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"harness/internal/config"
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

// RESULTS ARE CAPPED AT INGEST. Once a connection has refused a
// request by its size, no single tool result may take more than a quarter of that
// limit. A windowed tool is answered by the existing retry-smaller path, so the
// model re-reads in pieces; any other result keeps its head and tail.
func (r *Runner) byteCapResult(name string, args map[string]any, content string, ok bool, metadata map[string]any, limit int) (string, bool, map[string]any) {
	capBytes := limit / 4
	if limit <= 0 || len(content) <= capBytes {
		return content, ok, metadata
	}
	capped := cloneMetadata(metadata)
	capped["result_too_large"] = true
	capped["original_result_bytes"] = len(content)
	capped["result_byte_limit"] = capBytes
	windowed := ok && (name == "read_file" || name == "fetch_url" || name == "call_service")
	if _, batch := args["windows"]; windowed && batch {
		capped["retry_windows"] = "fewer_or_smaller"
		return fmt.Sprintf("error: read_file returned a windows batch of %d bytes, more than a quarter of this server's %d-byte request limit. Retry read_file with fewer or smaller windows.", len(content), limit), false, capped
	}
	if windowed {
		requested := integerArgument(args["limit"], r.cfg().Tools.ReadFile.DefaultLimit)
		if name == "fetch_url" {
			requested = integerArgument(args["limit"], r.cfg().Tools.Fetch.DefaultLimit)
		}
		retryLimit := max(1, int(int64(requested)*int64(capBytes)/int64(len(content))))
		offset := integerArgument(args["offset"], 1)
		capped["retry_offset"], capped["retry_limit"] = offset, retryLimit
		return fmt.Sprintf("error: %s returned %d bytes, more than a quarter of this server's %d-byte request limit. Retry %s with the same offset=%d and limit no greater than %d. Do not advance to next_offset until this window is read.", name, len(content), limit, name, offset, retryLimit), false, capped
	}
	keep := max(0, capBytes-200)
	head := strings.ToValidUTF8(content[:keep*2/3], "")
	tail := strings.ToValidUTF8(content[len(content)-(keep-keep*2/3):], "")
	return head + fmt.Sprintf("\n\n[tool result cut to fit the server's %d-byte request limit]\n\n", limit) + tail, ok, capped
}

func integerArgument(value any, fallback int) int {
	switch number := value.(type) {
	case int:
		return number
	case float64:
		return int(number)
	default:
		return fallback
	}
}

func cloneMetadata(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source)+5)
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
