package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"harness/internal/attachment"
	"harness/internal/config"
	"harness/internal/session"
)

type ReadFile struct {
	mu     sync.RWMutex
	cfg    config.ReadFileTool
	maxZIP int64
}

func NewReadFile(cfg config.ReadFileTool) *ReadFile { return &ReadFile{cfg: cfg, maxZIP: 8 << 20} }
func (*ReadFile) Name() string                      { return "read_file" }
func (*ReadFile) Description() string {
	return "Read numbered local UTF-8 text or list a .zip and read its members in memory. Use byte mode with byte offset/limit, line mode with one-based line/lines, or windows for multiple ordered labelled windows on one file. When more is true, pass returned next_offset as offset to advance or continue at next_line."
}
func (r *ReadFile) Schema() map[string]any {
	cfg := r.config()
	defaultLimit := min(cfg.DefaultLimit, cfg.MaxLimit)
	window := map[string]any{"type": "object", "properties": map[string]any{"label": map[string]any{"type": "string", "description": "Short result label"}, "offset": map[string]any{"type": "integer", "description": "One-based byte offset", "default": 1}, "limit": map[string]any{"type": "integer", "description": "Maximum bytes", "default": defaultLimit, "maximum": cfg.MaxLimit}, "line": map[string]any{"type": "integer", "description": "One-based starting line", "minimum": 1}, "lines": map[string]any{"type": "integer", "description": "Maximum lines", "default": 200, "minimum": 1, "maximum": 2000}}}
	return map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "description": "One-based byte offset; byte mode only", "default": 1}, "limit": map[string]any{"type": "integer", "description": "Maximum bytes to return; byte mode only", "default": defaultLimit, "maximum": cfg.MaxLimit}, "line": map[string]any{"type": "integer", "description": "One-based starting line; line mode only", "minimum": 1}, "lines": map[string]any{"type": "integer", "description": "Maximum lines to return; line mode only", "default": 200, "minimum": 1, "maximum": 2000}, "windows": map[string]any{"type": "array", "description": "Ordered byte or line windows on this file", "items": window, "minItems": 1, "maxItems": 32}}, "required": []string{"path"}}
}
func (r *ReadFile) Call(ctx context.Context, s *session.Session, args map[string]any) (string, error) {
	cfg := r.config()
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return "", fmt.Errorf("path is required")
	}
	archivePath, member, zipped := splitZIPPath(path)
	if !zipped {
		archivePath = path
	}
	root, rootErr := s.ReadRoot(archivePath)
	if rootErr != nil {
		return "", rootErr
	}
	resolved, err := resolveForSessionTool(ctx, s, root, archivePath)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(resolved); statErr == nil && info.IsDir() {
		return "path is a folder; use list_dir to inspect it.", nil
	}
	var data []byte
	if zipped {
		files, listing, zipErr := attachment.ReadZIP(resolved, r.zipLimit())
		if zipErr != nil {
			return "", zipErr
		}
		if member == "" {
			s.Touch(session.FileKey(resolved))
			return listing, nil
		}
		file, present := files[filepath.ToSlash(member)]
		if !present {
			return "", fmt.Errorf("zip member not found: %s", member)
		}
		if file.Refused != "" {
			return "", fmt.Errorf("zip member refused: %s", member)
		}
		data = file.Data
	} else {
		data, err = os.ReadFile(resolved)
		if err != nil {
			return "", err
		}
	}
	if zipped && attachment.Classify(member) == attachment.Office {
		if data, err = attachment.ExtractOfficeBytes(member, data, r.zipLimit()); err != nil {
			return "", err
		}
	}
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return "", fmt.Errorf("binary file refused: %s", path)
	}
	if rawWindows, batch := args["windows"]; batch {
		for _, key := range []string{"offset", "limit", "line", "lines"} {
			if _, mixed := args[key]; mixed {
				return "", fmt.Errorf("windows cannot be combined with top-level %s", key)
			}
		}
		windows, ok := rawWindows.([]any)
		if !ok || len(windows) < 1 || len(windows) > 32 {
			return "", fmt.Errorf("windows must contain between 1 and 32 entries")
		}
		results := make([]string, 0, len(windows))
		succeeded := false
		for index, rawWindow := range windows {
			windowArgs, ok := rawWindow.(map[string]any)
			label := fmt.Sprintf("window %d", index+1)
			if ok {
				if value, exists := windowArgs["label"].(string); exists && strings.TrimSpace(value) != "" {
					label = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
					labelRunes := []rune(label)
					if len(labelRunes) > 80 {
						label = string(labelRunes[:80])
					}
				}
			}
			if !ok {
				results = append(results, fmt.Sprintf("[read_file %s error]\nwindow must be an object", label))
				continue
			}
			result, windowErr := readFileWindow(string(data), windowArgs, cfg)
			if windowErr != nil {
				results = append(results, fmt.Sprintf("[read_file %s error]\n%s", label, windowErr))
				continue
			}
			succeeded = true
			results = append(results, fmt.Sprintf("[read_file %s]\n%s", label, result))
		}
		if succeeded {
			// Item 2mn (a): the seen-list is keyed by the FILE, the same key the
			// write coordinator records against, so a read clears a conflict
			// whichever root either session reached the file through.
			s.Touch(session.FileKey(resolved))
		}
		return ContentDigestLine(data) + strings.Join(results, "\n\n"), nil
	}
	result, err := readFileWindow(string(data), args, cfg)
	if err != nil {
		return "", err
	}
	s.Touch(session.FileKey(resolved))
	return ContentDigestLine(data) + result, nil
}

// ContentDigestLine is item 2mn (b): WHAT THIS READ SAW, in a form an edit can
// carry back. The whole FILE's digest heads every read - not the window's, because
// what an edit needs to know is whether the file changed underneath it, and a
// window can be identical while the rest of the file is rewritten.
//
// Twelve hex characters is 48 bits. It is here to catch a file that changed between
// a read and an edit, which is an accident and not an attack, and it costs one short
// line of a read the model was going to spend hundreds of tokens on.
func ContentDigestLine(data []byte) string {
	return fmt.Sprintf("[file sha256=%s]\n", ContentDigest(data))
}

// ContentDigest is the short digest read_file reports and edit_file compares.
func ContentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}

func readFileWindow(text string, args map[string]any, cfg config.ReadFileTool) (string, error) {
	if _, lineMode := args["line"]; lineMode {
		if _, byteOffset := args["offset"]; byteOffset {
			return "", fmt.Errorf("line mode cannot be combined with byte offset")
		}
		if _, byteLimit := args["limit"]; byteLimit {
			return "", fmt.Errorf("line mode cannot be combined with byte limit")
		}
		result, lineErr := numberedReadFileLines(text, number(args["line"], 1), number(args["lines"], 200))
		if lineErr != nil {
			return "", lineErr
		}
		return result, nil
	}
	if _, linesOnly := args["lines"]; linesOnly {
		return "", fmt.Errorf("lines requires line")
	}
	offset := number(args["offset"], 1)
	limit := number(args["limit"], cfg.DefaultLimit)
	limit = min(limit, cfg.MaxLimit)
	window, err := windowUTF8(text, offset, limit)
	if err != nil {
		return "", err
	}
	return numberedReadFileWindow(text, window), nil
}

func numberedReadFileLines(text string, line, count int) (string, error) {
	if line < 1 {
		return "", fmt.Errorf("line must be at least 1")
	}
	if count < 1 || count > 2000 {
		return "", fmt.Errorf("lines must be between 1 and 2000")
	}
	values := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") {
		values = values[:len(values)-1]
	}
	total := len(values)
	start := line - 1
	if start > total {
		return "", fmt.Errorf("line %d is past end of file (%d lines)", line, total)
	}
	end := min(total, start+count)
	more := end < total
	header := fmt.Sprintf("[line window: line=%d lines=%d total_lines=%d more=%t", line, end-start, total, more)
	if more {
		header += fmt.Sprintf(" next_line=%d", end+1)
	}
	header += "]"
	if start == end {
		return header, nil
	}
	numbered := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		numbered = append(numbered, fmt.Sprintf("%d: %s", index+1, strings.TrimSuffix(values[index], "\r")))
	}
	return header + "\n" + strings.Join(numbered, "\n"), nil
}

func numberedReadFileWindow(text string, window byteWindow) string {
	data := []byte(text)
	start := window.Offset - 1
	end := start + window.Bytes
	startLine := 1 + bytes.Count(data[:start], []byte{'\n'})
	startMidLine := start > 0 && data[start-1] != '\n'
	endMidLine := end < len(data) && (end == 0 || data[end-1] != '\n')
	header := byteWindowHeader(window)
	header = header[:len(header)-1] + fmt.Sprintf(" start_line=%d start_mid_line=%t end_mid_line=%t]", startLine, startMidLine, endMidLine)
	if window.Content == "" {
		return header
	}

	lines := strings.Split(window.Content, "\n")
	trailingNewline := strings.HasSuffix(window.Content, "\n")
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}
	numbered := make([]string, 0, len(lines))
	for index, line := range lines {
		numbered = append(numbered, fmt.Sprintf("%d: %s", startLine+index, strings.TrimSuffix(line, "\r")))
	}
	body := strings.Join(numbered, "\n")
	if trailingNewline {
		body += "\n"
	}
	return header + "\n" + body
}
func (r *ReadFile) Configure(value config.Config) {
	r.mu.Lock()
	r.cfg = value.Tools.ReadFile
	r.maxZIP = value.Tools.Attachments.MaxBytes
	r.mu.Unlock()
}

func splitZIPPath(value string) (string, string, bool) {
	slash := strings.ReplaceAll(value, `\`, "/")
	end := strings.Index(strings.ToLower(slash), ".zip/")
	if end < 0 && strings.HasSuffix(strings.ToLower(slash), ".zip") {
		end = len(slash) - 4
	}
	if end < 0 {
		return "", "", false
	}
	end += 4
	return filepath.FromSlash(slash[:end]), strings.TrimPrefix(slash[end:], "/"), true
}
func (r *ReadFile) config() config.ReadFileTool { r.mu.RLock(); defer r.mu.RUnlock(); return r.cfg }
func (r *ReadFile) zipLimit() int64             { r.mu.RLock(); defer r.mu.RUnlock(); return r.maxZIP }
func number(value any, fallback int) int {
	if value == nil {
		return fallback
	}
	if n, ok := value.(float64); ok {
		return int(n)
	}
	if n, ok := value.(int); ok {
		return n
	}
	return fallback
}
