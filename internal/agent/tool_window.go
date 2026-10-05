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

// cutNewestRunningResult is the last resort after ordinary compaction has left
// only the current turn. One newest result becomes its saved-file line, so the
// next budget pass can continue cutting only as far as necessary.
func (r *Runner) cutNewestRunningResult(ctx context.Context, s *session.Session, connection *config.Connection) bool {
	messages := s.MessagesCopy()
	current := runningTurnIDs(messages, s.RunPin())
	for index := len(messages) - 1; index >= 0; index-- {
		message := &messages[index]
		if message.Role != "tool" || message.Elided || !current[message.ID] {
			continue
		}
		note := ""
		for _, line := range strings.Split(message.Content, "\n") {
			if strings.HasPrefix(line, "[tool result cut:") {
				note = line
				break
			}
		}
		if note == message.Content {
			continue
		}
		if note == "" {
			path, offset, err := r.saveToolResult(s, message.Content)
			if err != nil {
				note = fmt.Sprintf("[tool result cut: %d bytes; saving the full output failed: %v]", len(message.Content), err)
			} else {
				note = fmt.Sprintf("[tool result cut: %d bytes; full output saved at %s; read_file path=%s offset=%d to page it]", len(message.Content), path, path, offset)
			}
		}
		message.Content = note
		message.Tokens, message.Estimated = r.count(ctx, connection, note)
		s.ReplaceMessages(messages)
		return true
	}
	return false
}

// Item 2o8 (c): RESULTS ARE CAPPED AT INGEST. Once a connection has refused a
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
	trimLargest([]*string{&content}, len(content)-capBytes, limit)
	return content, ok, capped
}

// readCutShortResult ends a read whose next window cannot fit the context a
// second time running (item 2fv): the compaction between the two refusals has
// had its chance, so rather than retry until the run stops for tool errors,
// the harness ends the read and the model answers from what it has read. The
// result is a note, not an error, and the run's next requests carry no tools.
func readCutShortResult(args, metadata map[string]any) (string, bool, map[string]any) {
	path, _ := args["path"].(string)
	where := fmt.Sprintf("offset=%d", integerArgument(args["offset"], 1))
	if _, lineMode := args["line"]; lineMode {
		where = fmt.Sprintf("line=%d", integerArgument(args["line"], 1))
	}
	needed, _ := metadata["original_result_tokens"].(int)
	available, _ := metadata["result_token_limit"].(int)
	content := fmt.Sprintf("note: the read was cut short: the context has no room for another window of %s at %s (%d tokens needed, %d available before the output reserve), so the harness has ended the read. Answer now from what you have read, and begin with one line saying the read was cut short and how far it got.", path, where, needed, available)
	cut := cloneMetadata(metadata)
	delete(cut, "retry_offset")
	delete(cut, "retry_limit")
	delete(cut, "retry_windows")
	cut["read_cut_short"] = true
	return content, true, cut
}

func (r *Runner) clampReadFileResult(ctx context.Context, s *session.Session, connection *config.Connection, args, metadata map[string]any, availableTokens int, operatorContext bool) (string, map[string]any, int, bool) {
	cfg := r.cfg().Tools.ReadFile
	field, unit, cursor := "limit", "bytes", "next_offset"
	requested := integerArgument(args[field], cfg.DefaultLimit)
	if _, lineMode := args["line"]; lineMode {
		field, unit, cursor = "lines", "lines", "next_line"
		requested = integerArgument(args[field], 200)
		requested = min(requested, 2000)
	} else {
		requested = min(requested, cfg.MaxLimit)
	}
	if requested < 1 {
		return "", nil, 0, false
	}

	low, high := 1, requested
	bestContent, bestTokens, bestLimit := "", 0, 0
	bestRemaining, bestNext := 0, 0
	for low <= high {
		limit := low + (high-low)/2
		candidateArgs := cloneMetadata(args)
		candidateArgs[field] = limit
		candidate, ok := r.repeatReadFile(ctx, s, candidateArgs, operatorContext)
		if !ok {
			return "", nil, 0, false
		}
		returned := headerInteger(candidate, unit)
		totalKey := "total"
		startKey := "offset"
		if unit == "lines" {
			totalKey, startKey = "total_lines", "line"
		}
		total := headerInteger(candidate, totalKey)
		start := headerInteger(candidate, startKey)
		next := headerInteger(candidate, cursor)
		remaining := max(0, total-(start-1+returned))
		note := fmt.Sprintf("[context clamp: requested_%s=%d returned_%s=%d remaining_%s=%d", unit, requested, unit, returned, unit, remaining)
		if next > 0 {
			note += fmt.Sprintf(" %s=%d", cursor, next)
		}
		note += "]\n"
		candidate = note + candidate
		tokens := r.textTokens(ctx, connection, candidate)
		if tokens <= availableTokens {
			bestContent, bestTokens, bestLimit = candidate, tokens, limit
			bestRemaining, bestNext = remaining, next
			low = limit + 1
		} else {
			high = limit - 1
		}
	}
	if bestLimit == 0 {
		return "", nil, 0, false
	}
	resultMetadata := cloneMetadata(metadata)
	resultMetadata["result_clamped"] = true
	resultMetadata["requested_"+field] = requested
	resultMetadata["returned_"+field] = bestLimit
	resultMetadata["remaining_"+unit] = bestRemaining
	if bestNext > 0 {
		resultMetadata[cursor] = bestNext
	}
	return bestContent, resultMetadata, bestTokens, true
}

func (r *Runner) repeatReadFile(ctx context.Context, s *session.Session, args map[string]any, operatorContext bool) (string, bool) {
	if operatorContext {
		return r.tools.CallAsOperator(ctx, s, "read_file", args)
	}
	outcome := r.tools.CallDetailed(ctx, s, "read_file", args)
	return outcome.Content, outcome.OK
}

func headerInteger(content, key string) int {
	header, _, _ := strings.Cut(content, "\n")
	for _, field := range strings.Fields(strings.Trim(header, "[]")) {
		name, value, found := strings.Cut(strings.TrimSuffix(field, "]"), "=")
		if found && name == key {
			var parsed int
			if _, err := fmt.Sscanf(value, "%d", &parsed); err == nil {
				return parsed
			}
		}
	}
	return 0
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
