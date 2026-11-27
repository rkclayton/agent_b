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
	baseDir  string
	userRoot string
	cfg      func() config.Config
	count    Counter
	mu       sync.Mutex
	baseMu   sync.RWMutex
}

func New(baseDir string, cfg func() config.Config, count Counter, sharedRoot ...string) *Manager {
	userRoot := baseDir
	if len(sharedRoot) != 0 && sharedRoot[0] != "" {
		userRoot = sharedRoot[0]
	}
	return &Manager{baseDir: baseDir, userRoot: userRoot, cfg: cfg, count: count}
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
	if root := RepoRoot(workspace); root != "" {
		return filepath.Join(root, ".agentb", "NOTES.md")
	}
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
	// and earlier hashed the display spelling. Keep that existing file
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

// RepoRoot finds the containing repository without invoking git.
func RepoRoot(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if info, statErr := os.Stat(abs); statErr == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return ""
		}
		abs = parent
	}
}

// EnsureRepoNotesIgnored keeps application-owned notes out of the tracked tree.
func EnsureRepoNotesIgnored(root string) error {
	git := filepath.Join(root, ".git")
	if info, err := os.Stat(git); err != nil || !info.IsDir() {
		return fmt.Errorf("repository metadata is unavailable")
	}
	path := filepath.Join(git, "info", "exclude")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(normalize(string(data)), "\n") {
		if strings.TrimSpace(line) == ".agentb/" {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(".agentb/\n")
	return err
}
func (m *Manager) AgentPath(agentID string) string {
	return filepath.Join(m.Dir(), "agent-"+config.AgentID(agentID)+".md")
}
func (m *Manager) SharedPath() string       { return filepath.Join(m.userRoot, "memory", "user.md") }
func (m *Manager) ProfileNotesPath() string { return filepath.Join(m.Dir(), "user.md") }

type ShareableNote struct {
	AgentID string `json:"agent_id"`
	Line    string `json:"line"`
}

func shareableNote(line string) bool {
	scope := parseNote(line).Scope
	return strings.HasPrefix(line, "- ") && (scope == "" || scope == "user" || scope == "agent")
}

func (m *Manager) ShareableNotes() ([]ShareableNote, error) {
	var notes []ShareableNote
	collect := func(id, path string) error {
		body, err := m.readPath(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(body, "\n") {
			if shareableNote(line) {
				notes = append(notes, ShareableNote{AgentID: id, Line: line})
			}
		}
		return nil
	}
	if local := m.ProfileNotesPath(); local != m.SharedPath() {
		if err := collect("", local); err != nil {
			return nil, err
		}
	}
	for _, agent := range m.cfg().Agents {
		id := config.AgentID(agent.Name)
		if err := collect(id, m.AgentPath(id)); err != nil {
			return nil, err
		}
	}
	return notes, nil
}

func (m *Manager) MoveToShared(note ShareableNote) error {
	cfg := m.cfg()
	_, agentKnown := cfg.Agent(note.AgentID)
	if (note.AgentID != "" && !agentKnown) || !shareableNote(note.Line) {
		return ErrNoteNotFound
	}
	source, destination := m.AgentPath(note.AgentID), m.SharedPath()
	if note.AgentID == "" {
		source = m.ProfileNotesPath()
		if source == destination {
			return ErrNoteNotFound
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	local, err := os.ReadFile(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	lines := strings.Split(normalize(string(local)), "\n")
	for i, line := range lines {
		if line != note.Line {
			continue
		}
		shared, err := os.ReadFile(destination)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		words := noteTextOf(line)
		var alreadyShared bool
		for _, existing := range strings.Split(normalize(string(shared)), "\n") {
			alreadyShared = alreadyShared || strings.EqualFold(noteTextOf(existing), words)
		}
		if !alreadyShared {
			next := strings.TrimRight(normalize(string(shared)), "\n")
			if next != "" {
				next += "\n"
			}
			next += line + "\n"
			if cfg.Memory.MaxTokens > 0 && (len([]rune(next))+3)/4 > cfg.Memory.MaxTokens {
				return fmt.Errorf("shared memory is full")
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return err
			}
			if err := replaceMemoryFile(destination, next); err != nil {
				return err
			}
		}
		return replaceMemoryFile(source, strings.Join(append(lines[:i], lines[i+1:]...), "\n"))
	}
	return nil
}

func replaceMemoryFile(path, body string) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
func (m *Manager) TargetPath(agentID string) string {
	if agent, ok := m.cfg().Agent(agentID); ok && agent.Private && config.AgentID(agent.Name) != "agent_b" {
		return m.AgentPath(agentID)
	}
	return m.SharedPath()
}
func (m *Manager) Load(ctx context.Context, workspace, connectionID string) (string, string, error) {
	path := m.Path(workspace)
	if info, err := os.Stat(path); err == nil && strings.EqualFold(filepath.Base(path), "NOTES.md") && info.Size() > 8*1024 {
		return "[Repository notes exceed 8 KB; read " + path + " when needed.]", path, nil
	}
	return m.load(ctx, path, connectionID, "Notes from earlier sessions in this folder:")
}
func (m *Manager) LoadAgent(ctx context.Context, agentID, connectionID string) (string, string, error) {
	shared, path, err := m.load(ctx, m.SharedPath(), connectionID, "Notes about the user:")
	if err != nil {
		return "", path, err
	}
	if local := m.ProfileNotesPath(); local != path {
		profile, _, err := m.load(ctx, local, connectionID, "Notes from this profile:")
		if err != nil {
			return shared, path, err
		}
		shared = strings.TrimSpace(shared + "\n\n" + profile)
	}
	agent, private := m.cfg().Agent(agentID)
	if !private || !agent.Private || config.AgentID(agent.Name) == "agent_b" {
		return shared, path, nil
	}
	own, _, err := m.load(ctx, m.AgentPath(agentID), connectionID, "Private notes for "+agent.Name+":")
	if err != nil || own == "" {
		return shared, path, err
	}
	return strings.TrimSpace(shared + "\n\n" + own), path, nil
}

func (m *Manager) load(ctx context.Context, path, connectionID, heading string) (string, string, error) {
	_ = ctx
	_ = connectionID
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
		lines = append(lines, "- "+line)
	}
	if err := scanner.Err(); err != nil {
		return "", path, err
	}
	return memoryBlock(heading, lines), path, nil
}

func (m *Manager) Read(workspace string) (string, error) {
	return m.readPath(m.Path(workspace))
}
func (m *Manager) ReadAgent(agentID string) (string, error) {
	return m.readPath(m.TargetPath(agentID))
}
func (m *Manager) ReadForAgent(agentID string) (string, error) {
	shared, err := m.readPath(m.SharedPath())
	if err != nil {
		return "", err
	}
	if local := m.ProfileNotesPath(); local != m.SharedPath() {
		profile, err := m.readPath(local)
		if err != nil {
			return "", err
		}
		shared = strings.TrimSpace(shared + "\n" + profile)
	}
	agent, ok := m.cfg().Agent(agentID)
	if !ok || !agent.Private || config.AgentID(agent.Name) == "agent_b" {
		return shared, nil
	}
	private, err := m.readPath(m.AgentPath(agentID))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(shared + "\n" + private), nil
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
	return m.clearPath(m.TargetPath(agentID))
}

func (m *Manager) DeleteAgent(agentID string) error { return m.clearPath(m.AgentPath(agentID)) }
func (m *Manager) RenameAgent(from, to string) error {
	old, next := m.AgentPath(from), m.AgentPath(to)
	if old == next {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(next), 0o700); err != nil {
		return err
	}
	err := os.Rename(old, next)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func (m *Manager) RemoveAgent(agentID, note string) (bool, error) {
	target := m.TargetPath(agentID)
	writes := []events.MemoryWrite{{Path: target, Note: note, Target: "agent", AgentID: agentID}}
	if local := m.ProfileNotesPath(); target == m.SharedPath() && local != target {
		writes = append(writes, events.MemoryWrite{Path: local, Note: note, Target: "agent", AgentID: agentID})
	}
	count, err := m.DropSessionWrites(writes)
	return count > 0, err
}

func (m *Manager) Count(workspace string) (int, error) {
	return m.countPath(m.Path(workspace))
}
func (m *Manager) CountAgent(agentID string) (int, error) {
	return m.countPath(m.TargetPath(agentID))
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
		if path != m.SharedPath() && (err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
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
				// a removal is undoable. The whole line goes to the
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

func memoryBlock(heading string, lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	out := []string{heading}
	out = append(out, lines...)
	return strings.Join(out, "\n")
}
func (m *Manager) Note(workspace, note string) (string, bool, error) {
	return m.notePath(m.Path(workspace), note)
}
func (m *Manager) NoteAgent(agentID, note string) (string, bool, error) {
	return m.notePath(m.TargetPath(agentID), note)
}

// a note is written rarely, scoped, replacing, and within a budget.
//
// Write carries what the note needs to be accountable for itself. Everything
// here is a property of the FILE rather than of prompt injection. The newest
// note is saved and oldest records are removed until the bounded file fits.
type Write struct {
	Note     string
	Scope    string // user | repository | environment
	Replaces string // the text of the note this supersedes, or empty
	Run      string
	Chat     string
	Turn     int
	// UntrustedInTurn marks a note written in a turn that had an untrusted tool
	// result in it. (e): a note that arrived beside external content is marked,
	// because that is the note somebody should look at twice.
	UntrustedInTurn bool
	// Budget is the layer's token budget for the FILE. Zero means unbounded.
	Budget int
}

// ErrNoteNotFound is (b): a replaces that names nothing is a mistake worth
// reporting rather than quietly becoming an ordinary write.
var ErrNoteNotFound = errors.New("the note to replace was not found")
var ErrScopeMismatch = errors.New("replacement scope mismatch")

// WriteNote preserves the original API for reflection and import callers.
func (m *Manager) WriteNote(path string, write Write) (bool, error) {
	duplicate, _, err := m.WriteNoteResult(path, write)
	return duplicate, err
}

// WriteNoteResult saves the new note, then evicts oldest notes until the layer
// fits. The newest note is retained even when it alone exceeds a configured
// budget: remember never refuses the fact it was asked to save.
func (m *Manager) WriteNoteResult(path string, write Write) (bool, int, error) {
	note := strings.TrimSpace(write.Note)
	if note == "" {
		return false, 0, fmt.Errorf("note is empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, 0, err
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
			return true, 0, nil
		}
	}
	// the old note goes in the SAME write. A replace that left the old one
	// behind would grow the file it was meant to hold steady.
	var archived string
	if replaced := strings.TrimSpace(write.Replaces); replaced != "" {
		query := normalizedWords(replaced)
		matches := []int{}
		for index, line := range lines {
			if strings.Contains(normalizedWords(noteTextOf(line)), query) {
				matches = append(matches, index)
			}
		}
		if len(matches) != 1 {
			shown := matches
			kind := "matched more than one note"
			if len(matches) == 0 {
				shown = make([]int, len(lines))
				for index := range lines {
					shown[index] = index
				}
				kind = "matched no note"
			}
			previews := make([]string, 0, len(shown))
			for _, index := range shown {
				previews = append(previews, firstNoteWords(noteTextOf(lines[index]), 8))
			}
			return false, 0, fmt.Errorf("%w: %q %s; notes: %s", ErrNoteNotFound, replaced, kind, strings.Join(previews, "; "))
		}
		matched := matches[0]
		oldScope := parseNote(lines[matched]).Scope
		if oldScope != write.Scope {
			return false, 0, fmt.Errorf("%w: existing note is %s; new note is %s", ErrScopeMismatch, scopeName(oldScope), scopeName(write.Scope))
		}
		archived = lines[matched]
		kept := append([]string(nil), lines[:matched]...)
		kept = append(kept, lines[matched+1:]...)
		lines = kept
	}
	lines = append(lines, formatNote(note, write))
	dropped := 0
	for write.Budget > 0 && len(lines) > 1 && (len([]rune(strings.Join(lines, "\n")))+3)/4 > write.Budget {
		lines = lines[1:]
		dropped++
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, dropped, err
	}
	if archived == "" && dropped == 0 {
		file, openErr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if openErr != nil {
			return false, 0, openErr
		}
		prefix := ""
		if len(data) > 0 && data[len(data)-1] != '\n' {
			prefix = "\n"
		}
		_, writeErr := file.WriteString(prefix + lines[len(lines)-1] + "\n")
		closeErr := file.Close()
		if writeErr != nil {
			return false, 0, writeErr
		}
		if closeErr != nil {
			return false, 0, closeErr
		}
	} else if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return false, dropped, err
	}
	if archived != "" {
		return false, dropped, appendReplacement(path, archived, write)
	}
	return false, dropped, nil
}

func normalizedWords(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
func firstNoteWords(value string, count int) string {
	words := strings.Fields(value)
	if len(words) > count {
		words = words[:count]
	}
	return strings.Join(words, " ")
}

func scopeName(value string) string {
	if value == "" {
		return "unscoped"
	}
	return value
}

func appendReplacement(path, line string, write Write) error {
	ext := filepath.Ext(path)
	archive := strings.TrimSuffix(path, ext) + "-replaced.md"
	data, err := os.ReadFile(archive)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	rows := []string{}
	for _, row := range strings.Split(normalize(string(data)), "\n") {
		if strings.TrimSpace(row) != "" {
			rows = append(rows, row)
		}
	}
	old := parseNote(line)
	rows = append(rows, fmt.Sprintf("- %s %s  [scope: %s, chat: %s]", time.Now().Format("2006-01-02"), old.Text, old.Scope, write.Chat))
	if len(rows) > 50 {
		rows = rows[len(rows)-50:]
	}
	return os.WriteFile(archive, []byte(strings.Join(rows, "\n")+"\n"), 0o600)
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
	// reflection's own state, "unconfirmed" or "confirmed <date>",
	// read from the same trailing provenance the other fields come from.
	Reflection string `json:"reflection,omitempty"`
}

// Notes reads a layer back. the Settings view lists them with
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
		// DEDUPE BEFORE WRITING, on meaning rather than on bytes. This
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
