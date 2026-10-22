package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"harness/internal/events"
	"harness/internal/session"
)

type FileCoordinator struct {
	workspaces  *session.WorkspaceRegistry
	label       func(string) string
	bus         *events.Bus
	undo        *FileUndoStore
	diagnostics *languageDiagnostics
}

type FileBatchChange struct {
	Path   string
	Data   []byte
	Mode   os.FileMode
	Remove bool
}

// ApplyBatch records one helper return as one run-level undo group. The caller
// has already checked every baseline; rollback keeps a mid-apply I/O failure
// from exposing part of the helper's result.
func (c *FileCoordinator) ApplyBatch(s *session.Session, changes []FileBatchChange) error {
	type prior struct {
		data    []byte
		mode    os.FileMode
		existed bool
	}
	before := make([]prior, len(changes))
	for i, change := range changes {
		info, err := os.Stat(change.Path)
		if err == nil {
			before[i].data, err = os.ReadFile(change.Path)
			before[i].mode, before[i].existed = info.Mode(), true
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := c.prepareUndo(s, change.Path); err != nil {
			return err
		}
	}
	rollback := func(last int) {
		for i := last; i >= 0; i-- {
			if before[i].existed {
				_ = os.MkdirAll(filepath.Dir(changes[i].Path), 0o755)
				_ = os.WriteFile(changes[i].Path, before[i].data, before[i].mode.Perm())
			} else {
				_ = os.Remove(changes[i].Path)
			}
		}
	}
	for i, change := range changes {
		var err error
		if change.Remove {
			err = os.Remove(change.Path)
			if os.IsNotExist(err) {
				err = nil
			}
		} else {
			err = os.MkdirAll(filepath.Dir(change.Path), 0o755)
			if err == nil {
				mode := change.Mode.Perm()
				if mode == 0 {
					mode = 0o644
				}
				err = os.WriteFile(change.Path, change.Data, mode)
			}
		}
		if err != nil {
			rollback(i)
			return err
		}
	}
	for _, change := range changes {
		if err := c.commitUndo(s, change.Path); err != nil {
			return fmt.Errorf("record undo state: %w", err)
		}
		c.record(s, change.Path)
	}
	return nil
}

func NewFileCoordinator(workspaces *session.WorkspaceRegistry, label func(string) string, bus *events.Bus) *FileCoordinator {
	return &FileCoordinator{workspaces: workspaces, label: label, bus: bus}
}
func (c *FileCoordinator) SetUndoStore(store *FileUndoStore)         { c.undo = store }
func (c *FileCoordinator) SetDiagnostics(value *languageDiagnostics) { c.diagnostics = value }
func (c *FileCoordinator) prepareUndo(s *session.Session, resolved string) error {
	if c == nil || c.undo == nil {
		return nil
	}
	runID := s.Snapshot().Run.RunID
	return c.undo.Prepare(s.ID, runID, resolved, workspaceRel(s.Workspace, resolved))
}
func (c *FileCoordinator) commitUndo(s *session.Session, resolved string) error {
	if c == nil || c.undo == nil {
		return nil
	}
	return c.undo.Commit(s.ID, s.Snapshot().Run.RunID, resolved)
}
func cleanRel(path string) string { return filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) }
func workspaceRel(workspace, resolved string) string {
	rel, err := filepath.Rel(workspace, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return cleanRel(resolved)
	}
	return cleanRel(rel)
}
func (c *FileCoordinator) check(s *session.Session, path, resolved string) (string, error) {
	root := s.Workspace
	if s.Role == "d" {
		root = s.PlanDir
	}
	// the CONFLICT is checked on the file's own identity, so a
	// shared plan repository is shared however each session reached it. `rel`
	// stays what the operator reads in the event, which is (d): the event's shape
	// and the chat's rendering of it are unchanged.
	rel := workspaceRel(root, resolved)
	seen, hasSeen := s.LastSeenAt(session.FileKey(resolved))
	if record, ok := c.workspaces.LastWriter(resolved); ok && record.SessionID != s.ID && (!hasSeen || record.At.After(seen)) {
		age := int(time.Since(record.At).Seconds())
		if age < 0 {
			age = 0
		}
		label := c.label(record.SessionID)
		c.bus.Publish(events.New(events.WorkspaceConflict, s.ID, "", map[string]any{"path": rel, "session_id": s.ID, "other_session_id": record.SessionID, "other_label": label, "age_s": age}))
		return "", fmt.Errorf("session %s wrote this file %ds ago; re-read before editing.", label, age)
	}
	if info, err := os.Stat(resolved); err == nil && hasSeen && info.ModTime().After(seen) {
		return "note: file changed since you last read it.\n", nil
	}
	return "", nil
}

// lastWriterNote names who wrote a file last, for the refusal. It says
// nothing when the last writer was this session or when nothing is recorded: "it
// changed" is the fact, and naming a session that did not do it would be worse than
// naming none.
func (c *FileCoordinator) lastWriterNote(s *session.Session, resolved string) string {
	if c == nil || s == nil {
		return ""
	}
	record, ok := c.workspaces.LastWriter(resolved)
	if !ok || record.SessionID == s.ID {
		return ""
	}
	age := int(time.Since(record.At).Seconds())
	if age < 0 {
		age = 0
	}
	return fmt.Sprintf("; session %s wrote it %ds ago", c.label(record.SessionID), age)
}

func (c *FileCoordinator) record(s *session.Session, resolved string) {
	// the write is recorded against the FILE. The session's own root
	// no longer takes part, which is why a shared plan repository is now shared.
	s.Touch(resolved)
	c.workspaces.RecordWrite(resolved, s.ID)
	c.publishPlan(s)
}

func (c *FileCoordinator) publishPlan(s *session.Session) {
	if s.Role == "d" {
		s.RefreshPlanName()
		snapshot := s.Snapshot()
		c.bus.Publish(events.New(events.SessionUpdated, s.ID, "", map[string]any{"role": snapshot.Role, "plan_id": snapshot.PlanID, "plan_name": snapshot.PlanName, "plan_dir": snapshot.PlanDir}))
	}
}

type WriteFile struct{ coordinator *FileCoordinator }

func NewWriteFile(c *FileCoordinator) *WriteFile { return &WriteFile{coordinator: c} }
func (*WriteFile) Name() string                  { return "write_file" }
func (*WriteFile) Description() string {
	return "Create or fully replace the file at path with content, creating parent directories. Unlike edit_file, it writes the whole file. A path ending .xlsx takes Markdown tables, one per ## sheet-name heading, and writes an Excel workbook."
}
func (*WriteFile) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "required": []string{"path", "content"}}
}
func (w *WriteFile) Call(ctx context.Context, s *session.Session, args map[string]any) (string, error) {
	if s.Role == "d" && !s.PlanWriteAllowed() {
		return "", fmt.Errorf("plan-page writes require accepting a proposal")
	}
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return "", fmt.Errorf("path is required")
	}
	// A planner writing its plan.md takes the one plan-file lock every other
	// writer of that file takes, so its edit and a worker's marker serialise.
	if s.Role == "d" {
		if planFile, isPlan := s.PlanFileFor(path); isPlan {
			unlock := session.LockPlanFileWrite(planFile)
			defer unlock()
		}
	}
	content, ok := args["content"].(string)
	if !ok {
		return "", fmt.Errorf("content is required")
	}
	if s.Role == "d" {
		if err := refuseRepoPolicyWrite(".", path); err != nil {
			return "", err
		}
	}
	root, rootErr := s.WriteRoot(path)
	if rootErr != nil {
		return "", rootErr
	}
	if err := refuseRepoPolicyWrite(root, path); err != nil {
		return "", err
	}
	resolved, err := resolveForWorkerWrite(ctx, s, root, path)
	if err != nil {
		return "", err
	}
	if err := refuseRepoPolicyWrite(root, resolved); err != nil {
		return "", err
	}
	if err := refusePlanManifestWrite(s, resolved); err != nil {
		return "", err
	}
	// an .xlsx path turns its Markdown tables into a workbook; every
	// other extension is written as the bytes given.
	sheetSummary := ""
	if strings.EqualFold(filepath.Ext(resolved), ".xlsx") {
		sheets, parseErr := markdownWorkbook(content)
		if parseErr != nil {
			return "", parseErr
		}
		workbook, buildErr := buildWorkbook(sheets)
		if buildErr != nil {
			return "", buildErr
		}
		rows := 0
		for _, sheet := range sheets {
			rows += len(sheet.rows)
		}
		sheetSummary = fmt.Sprintf("%d %s, %d rows", len(sheets), map[bool]string{true: "sheet", false: "sheets"}[len(sheets) == 1], rows)
		content = string(workbook)
	}
	if existing, readErr := os.ReadFile(resolved); readErr == nil && string(existing) == content {
		w.coordinator.publishPlan(s)
		return fmt.Sprintf("unchanged: %s already has the requested bytes", cleanRel(path)), nil
	}
	prefix, err := w.coordinator.check(s, path, resolved)
	if err != nil {
		return "", err
	}
	fail := func(cause error) (string, error) {
		if prefix != "" {
			return "", fmt.Errorf("%serror: %v", prefix, cause)
		}
		return "", cause
	}
	commitState, err := s.ReserveSkillStateWrite(resolved, int64(len(content)))
	if err != nil {
		return fail(err)
	}
	defer commitState(false)
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return fail(err)
	}
	temp, err := os.CreateTemp(filepath.Dir(resolved), ".agentb-write-*")
	if err != nil {
		return fail(err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err = temp.WriteString(content); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fail(err)
	}
	if err := w.coordinator.prepareUndo(s, resolved); err != nil {
		return fail(err)
	}
	if err := AtomicReplace(tempPath, resolved); err != nil {
		return fail(err)
	}
	if err := w.coordinator.commitUndo(s, resolved); err != nil {
		return fail(fmt.Errorf("record undo state: %w", err))
	}
	commitState(true)
	w.coordinator.record(s, resolved)
	lines := 0
	if content != "" {
		lines = strings.Count(content, "\n") + 1
		if strings.HasSuffix(content, "\n") {
			lines--
		}
	}
	if sheetSummary != "" {
		return prefix + fmt.Sprintf("ok: wrote %s (%s)", cleanRel(path), sheetSummary), nil
	}
	result := prefix + fmt.Sprintf("ok: wrote %s (%d lines)", cleanRel(path), lines)
	if w.coordinator != nil && w.coordinator.diagnostics != nil {
		if check, ok := w.coordinator.diagnostics.Check(ctx, root, resolved); ok && check != "" {
			result += "\n\n" + check
		}
	}
	return result, nil
}
