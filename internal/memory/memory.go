package memory

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"harness/internal/config"
	"harness/internal/events"
)

type Counter func(context.Context, string, string) (int, error)
type Manager struct {
	baseDir string
	cfg     func() config.Config
	count   Counter
	mu      sync.Mutex
	baseMu  sync.RWMutex
}

func New(baseDir string, cfg func() config.Config, count Counter) *Manager {
	return &Manager{baseDir: baseDir, cfg: cfg, count: count}
}
func (m *Manager) SetBaseDir(baseDir string) {
	m.baseMu.Lock()
	m.baseDir = filepath.Clean(baseDir)
	m.baseMu.Unlock()
}
func (m *Manager) Dir() string {
	m.baseMu.RLock()
	baseDir := m.baseDir
	m.baseMu.RUnlock()
	dir := m.cfg().Memory.Dir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(baseDir, dir)
	}
	return filepath.Clean(dir)
}
func (m *Manager) Path(workspace string) string {
	abs, _ := filepath.Abs(workspace)
	clean := filepath.Clean(abs)
	canonical := filepath.ToSlash(clean)
	if filepath.Separator == '\\' {
		canonical = strings.ToLower(canonical)
	}
	sum := sha256.Sum256([]byte(canonical))
	name := filepath.Base(abs) + "-" + hex.EncodeToString(sum[:4]) + ".md"
	dir := m.Dir()
	path := filepath.Join(dir, name)
	// v0.9 and earlier hashed the display spelling. Keep that existing file
	// mapped to the default workspace instead of silently orphaning memory.
	legacySum := sha256.Sum256([]byte(clean))
	legacy := filepath.Join(dir, filepath.Base(abs)+"-"+hex.EncodeToString(legacySum[:4])+".md")
	if path != legacy {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if info, legacyErr := os.Stat(legacy); legacyErr == nil && info.Mode().IsRegular() {
				return legacy
			}
		}
	}
	return path
}
func (m *Manager) AgentPath(agentID string) string {
	return filepath.Join(m.Dir(), "agent-"+config.AgentID(agentID)+".md")
}
func (m *Manager) Load(ctx context.Context, workspace, connectionID string) (string, string, error) {
	path := m.Path(workspace)
	return m.load(ctx, path, connectionID, "Notes from earlier sessions in this folder:")
}
func (m *Manager) LoadAgent(ctx context.Context, agentID, connectionID string) (string, string, error) {
	path := m.AgentPath(agentID)
	return m.load(ctx, path, connectionID, "Notes about how this agent works with the user:")
}
func (m *Manager) load(ctx context.Context, path, connectionID, heading string) (string, string, error) {
	if !m.cfg().Memory.Enabled {
		return "", path, nil
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return "", path, nil
	}
	if err != nil {
		return "", path, err
	}
	defer file.Close()
	lines := []string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 && len(parts[0]) == 10 && parts[0][4] == '-' && parts[0][7] == '-' {
			line = parts[1]
		}
		if AboutAgentNote(line) {
			line = strings.TrimSuffix(line, "]")
			if strings.Contains(line, "[") {
				line += ", about-agent: yes]"
			} else {
				line += "  [about-agent: yes]"
			}
		}
		lines = append(lines, "- "+line)
	}
	if err := scanner.Err(); err != nil {
		return "", path, err
	}
	maxTokens := m.cfg().Memory.MaxTokens
	dropped := 0
	for len(lines) > 0 {
		body := memoryBlock(heading, layerName(heading), lines, dropped)
		tokens, countErr := m.count(ctx, connectionID, body)
		if countErr != nil {
			tokens = (len([]rune(body)) + 3) / 4
		}
		if maxTokens <= 0 || tokens <= maxTokens {
			return body, path, nil
		}
		lines = lines[1:]
		dropped++
	}
	return "", path, nil
}

// AboutAgentNote identifies avoidance rules about this harness, not project facts.
func AboutAgentNote(note string) bool {
	value := strings.ToLower(noteTextOf(note))
	subject := aboutAgentSubject.MatchString(value)
	avoid := aboutAgentAvoidance.MatchString(value)
	return subject && avoid
}

// FilterRecall removes display-marked harness notes before a model sees memory.
func FilterRecall(block string) string {
	lines := strings.Split(block, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !strings.Contains(line, "about-agent: yes") && !AboutAgentNote(line) {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func (m *Manager) Read(workspace string) (string, error) {
	return m.readPath(m.Path(workspace))
}
func (m *Manager) ReadAgent(agentID string) (string, error) {
	return m.readPath(m.AgentPath(agentID))
}
func (m *Manager) readPath(path string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(normalize(string(data)), "\n"), nil
}

func (m *Manager) Clear(workspace string) error {
	return m.clearPath(m.Path(workspace))
}
func (m *Manager) ClearAgent(agentID string) error {
	return m.clearPath(m.AgentPath(agentID))
}
func (m *Manager) RemoveAgent(agentID, note string) (bool, error) {
	count, err := m.DropSessionWrites([]events.MemoryWrite{{Path: m.AgentPath(agentID), Note: note, Target: "agent", AgentID: agentID}})
	return count > 0, err
}

func (m *Manager) Count(workspace string) (int, error) {
	return m.countPath(m.Path(workspace))
}
func (m *Manager) CountAgent(agentID string) (int, error) {
	return m.countPath(m.AgentPath(agentID))
}
func (m *Manager) countPath(path string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, line := range strings.Split(normalize(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, nil
}

func (m *Manager) DropSessionWrites(writes []events.MemoryWrite) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byPath := map[string]map[string]bool{}
	root := m.Dir()
	for _, write := range writes {
		path := filepath.Clean(write.Path)
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return 0, fmt.Errorf("memory write path is outside the memory root")
		}
		if byPath[path] == nil {
			byPath[path] = map[string]bool{}
		}
		byPath[path][strings.TrimSpace(write.Note)] = true
	}
	dropped := 0
	removed := map[string][]string{}
	for path, notes := range byPath {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return dropped, err
		}
		kept := []string{}
		for _, line := range strings.Split(strings.TrimRight(normalize(string(data)), "\n"), "\n") {
			plain := strings.TrimPrefix(strings.TrimSpace(line), "- ")
			parts := strings.SplitN(plain, " ", 2)
			note := plain
			if len(parts) == 2 && len(parts[0]) == 10 && parts[0][4] == '-' && parts[0][7] == '-' {
				note = parts[1]
			}
			if notes[strings.TrimSpace(note)] {
				dropped++
				// Item 2mw (c): a removal is undoable. The whole line goes to the
				// tombstone, so a restore brings back its words rather than a
				// reconstruction of them.
				removed[path] = append(removed[path], line)
				continue
			}
			if strings.TrimSpace(line) != "" {
				kept = append(kept, line)
			}
		}
		if len(kept) == 0 {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return dropped, err
			}
			continue
		}
		temporary := path + ".tmp"
		if err := os.WriteFile(temporary, []byte(strings.Join(kept, "\n")+"\n"), 0o600); err != nil {
			return dropped, err
		}
		if err := os.Rename(temporary, path); err != nil {
			return dropped, err
		}
	}
	// The tombstones are written after the layers, so a failure here loses the undo
	// and never the removal the operator asked for.
	for path, lines := range removed {
		if err := m.rememberRemoved(path, lines); err != nil {
			return dropped, err
		}
	}
	return dropped, nil
}
func (m *Manager) clearPath(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// layerName turns the injected heading into the words the model and the
// operator both use for that layer, so the notice says which one is full.
func layerName(heading string) string {
	if strings.Contains(strings.ToLower(heading), "agent") {
		return "agent memory"
	}
	return "folder memory"
}

func memoryBlock(heading, layer string, lines []string, dropped int) string {
	if len(lines) == 0 {
		return ""
	}
	out := []string{heading}
	if dropped > 0 {
		out = append(out, fmt.Sprintf("[%d older %s notes are omitted here because the layer is over its budget. Nothing has been deleted. When you have a spare turn, consolidate the oldest notes into fewer lines with remember, keeping every fact that still holds.]", dropped, layer))
	}
	out = append(out, lines...)
	return strings.Join(out, "\n")
}
func (m *Manager) Note(workspace, note string) (string, bool, error) {
	return m.notePath(m.Path(workspace), note)
}
func (m *Manager) NoteAgent(agentID, note string) (string, bool, error) {
	return m.notePath(m.AgentPath(agentID), note)
}

// Item 2jf: a note is written rarely, scoped, replacing, and within a budget.
//
// Write carries what the note needs to be accountable for itself. Everything
// here is a property of the FILE rather than of the injection: item 2eb trimmed
// the oldest notes on the way IN and asked the model to consolidate, which meant
// the file grew forever and the operator never met the limit. (d) reverses that
// — the write is refused and the model is told to replace something it names.
type Write struct {
	Note     string
	Scope    string // user | repository | environment
	Replaces string // the text of the note this supersedes, or empty
	Run      string
	Turn     int
	// UntrustedInTurn marks a note written in a turn that had an untrusted tool
	// result in it. (e): a note that arrived beside external content is marked,
	// because that is the note somebody should look at twice.
	UntrustedInTurn bool
	// Budget is the layer's token budget for the FILE. Zero means unbounded.
	Budget int
}

// ErrMemoryFull is (d): the write is refused and the caller is told to replace a
// note it names. The harness never trims.
var ErrMemoryFull = errors.New("memory full")

// ErrNoteNotFound is (b): a replaces that names nothing is a mistake worth
// reporting rather than quietly becoming an ordinary write.
var ErrNoteNotFound = errors.New("the note to replace was not found")

// WriteNote does (b), (d) and (e) in ONE pass over the file, because a replace
// and a budget check that ran separately could each pass and together overflow.
func (m *Manager) WriteNote(path string, write Write) (bool, error) {
	note := strings.TrimSpace(write.Note)
	if note == "" {
		return false, fmt.Errorf("note is empty")
	}
	if len([]rune(note)) > 300 {
		return false, fmt.Errorf("note too long (max 300 Unicode characters)")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	lines := []string{}
	for _, line := range strings.Split(normalize(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	// A duplicate is still a duplicate, and says so rather than being written
	// twice or refused as full.
	for _, line := range lines {
		if strings.EqualFold(noteTextOf(line), note) {
			return true, nil
		}
	}
	// (b): the old note goes in the SAME write. A replace that left the old one
	// behind would grow the file it was meant to hold steady.
	if replaced := strings.TrimSpace(write.Replaces); replaced != "" {
		kept := make([]string, 0, len(lines))
		found := false
		for _, line := range lines {
			if !found && strings.EqualFold(noteTextOf(line), replaced) {
				found = true
				continue
			}
			kept = append(kept, line)
		}
		if !found {
			return false, fmt.Errorf("%w: %q", ErrNoteNotFound, replaced)
		}
		lines = kept
	}
	lines = append(lines, formatNote(note, write))
	// (d): the budget is on the FILE, and a write that would exceed it is
	// refused with what to do about it. The estimate is the same crude
	// characters-over-four the loader falls back to, so the two agree.
	if write.Budget > 0 {
		body := strings.Join(lines, "\n")
		if tokens := (len([]rune(body)) + 3) / 4; tokens > write.Budget {
			return false, fmt.Errorf("%w: this layer would hold %d of %d tokens; replace a note you name, or skip", ErrMemoryFull, tokens, write.Budget)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return false, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// formatNote is the line as it lands in the file: the existing date-and-text
// shape with (e)'s provenance appended, so the FILE records where the note came
// from. Anything that read the old format still reads this.
func formatNote(note string, write Write) string {
	line := fmt.Sprintf("- %s %s", time.Now().Format("2006-01-02"), note)
	marks := []string{}
	if write.Scope != "" {
		marks = append(marks, "scope: "+write.Scope)
	}
	if write.Run != "" {
		marks = append(marks, "run: "+write.Run)
	}
	if write.Turn > 0 {
		marks = append(marks, fmt.Sprintf("turn: %d", write.Turn))
	}
	if write.UntrustedInTurn {
		marks = append(marks, "untrusted-in-turn: yes")
	}
	if len(marks) == 0 {
		return line
	}
	return line + "  [" + strings.Join(marks, ", ") + "]"
}

// Note is one line of a layer, parsed back out for the Settings view.
type Note struct {
	Date            string `json:"date"`
	Text            string `json:"text"`
	Scope           string `json:"scope,omitempty"`
	Run             string `json:"run,omitempty"`
	Turn            int    `json:"turn,omitempty"`
	UntrustedInTurn bool   `json:"untrusted_in_turn,omitempty"`
	// Item 2mw (b): reflection's own state, "unconfirmed" or "confirmed <date>",
	// read from the same trailing provenance the other fields come from.
	Reflection string `json:"reflection,omitempty"`
}

// Notes reads a layer back. Item 2jf (f): the Settings view lists them with
// their scope, date and provenance, so the operator can see what the agent
// believes and where each belief came from.
func (m *Manager) Notes(path string) ([]Note, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Note{}
	for _, line := range strings.Split(normalize(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, parseNote(line))
	}
	return out, nil
}

// noteTextOf is the note's own words, with the date and any provenance removed,
// so a replaces or a duplicate check compares what the model actually wrote.
func noteTextOf(line string) string { return parseNote(line).Text }

var notePattern = regexp.MustCompile(`^-\s*(\d{4}-\d{2}-\d{2})?\s*(.*)$`)
var provenancePattern = regexp.MustCompile(`\s*\[([^\]]*)\]\s*$`)
var aboutAgentSubject = regexp.MustCompile(`\b(read_file|list_dir|write_file|edit_file|search|shell|remember|recall|fetch_url|web_search|run_script|call_service|delegate|card|boundary|approval|workspace|sandbox|container|tool)s?\b`)
var aboutAgentAvoidance = regexp.MustCompile(`\b(do not|don't|never|avoid|refuse|stay|stop using|cannot|can't|wedge[sd]?|hangs?|fails?|broken)\b`)

func parseNote(line string) Note {
	note := Note{}
	rest := strings.TrimSpace(line)
	if match := notePattern.FindStringSubmatch(rest); match != nil {
		note.Date, rest = match[1], strings.TrimSpace(match[2])
	}
	if match := provenancePattern.FindStringSubmatch(rest); match != nil {
		rest = strings.TrimSpace(strings.TrimSuffix(rest, match[0]))
		for _, field := range strings.Split(match[1], ",") {
			key, value, found := strings.Cut(strings.TrimSpace(field), ":")
			if !found {
				continue
			}
			value = strings.TrimSpace(value)
			switch strings.TrimSpace(key) {
			case "scope":
				note.Scope = value
			case "run":
				note.Run = value
			case "turn":
				if parsed, err := strconv.Atoi(value); err == nil {
					note.Turn = parsed
				}
			case "untrusted-in-turn":
				note.UntrustedInTurn = value == "yes"
			case "reflection":
				note.Reflection = value
			}
		}
	}
	note.Text = rest
	return note
}

func (m *Manager) notePath(path, note string) (string, bool, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return "", false, fmt.Errorf("note is empty")
	}
	if len([]rune(note)) > 300 {
		return "", false, fmt.Errorf("note too long (max 300 Unicode characters)")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return path, false, err
	}
	for _, line := range strings.Split(normalize(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Item 2mw (d): DEDUPE BEFORE WRITING, on meaning rather than on bytes. This
		// compared the remainder of the line against the incoming note, so differing
		// whitespace wrote a second copy of the same correction — and, while
		// reflection's marker was a prefix, a note whose provenance changed looked new.
		if SameNote(line, note) {
			return path, true, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, false, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return path, false, err
	}
	_, writeErr := fmt.Fprintf(file, "- %s %s\n", time.Now().Format("2006-01-02"), note)
	closeErr := file.Close()
	if writeErr != nil {
		return path, false, writeErr
	}
	return path, false, closeErr
}
func normalize(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}
