package operatorfiles

import (
	"bytes"
	"log"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/session"
)

func testManager(t *testing.T) (*Manager, *config.Config) {
	t.Helper()
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	cfg := config.Defaults(filepath.Join(root, "workspace"))
	manager := New(root, logDir, func() config.Config { return cfg })
	if err := manager.Ensure(); err != nil {
		t.Fatal(err)
	}
	return manager, &cfg
}

func writeInbox(t *testing.T, manager *Manager, value string) {
	t.Helper()
	if err := os.WriteFile(manager.InboxPath(), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInboxVerbsTaskDelayAndNote(t *testing.T) {
	manager, _ := testManager(t)
	writeInbox(t, manager, "REVISE: use the smaller patch\nand keep tests\n")
	action, err := manager.CheckInbox("s1", false)
	if err != nil || action.Revision != "use the smaller patch\nand keep tests" {
		t.Fatalf("revision=%q err=%v", action.Revision, err)
	}
	writeInbox(t, manager, "TASK: add the export golden\n")
	if _, err := manager.CheckInbox("s1", false); err != nil {
		t.Fatal(err)
	}
	plan, err := os.ReadFile(filepath.Join(manager.Root(), "PLAN.md"))
	if err != nil || string(plan) != "- [ ] add the export golden\n" {
		t.Fatalf("plan=%q err=%v", plan, err)
	}
	writeInbox(t, manager, "DELAY: 250ms\n")
	action, err = manager.CheckInbox("s1", false)
	if err != nil || action.Delay != 250*time.Millisecond {
		t.Fatalf("delay=%s err=%v", action.Delay, err)
	}
	writeInbox(t, manager, "Remember the release screenshot\n")
	action, err = manager.CheckInbox("s1", false)
	if err != nil || action.Revision != "Remember the release screenshot" {
		t.Fatalf("note=%q err=%v", action.Revision, err)
	}
	writeInbox(t, manager, "ANSWER: operator_mode\n")
	if action, err := manager.CheckInbox("s1", true); err != nil || action.Decision != "" {
		t.Fatalf("retired decision=%q err=%v", action.Decision, err)
	}
	writeInbox(t, manager, "ANSWER: run\n")
	if action, err := manager.CheckInbox("s1", true); err != nil || action.Decision != "" {
		t.Fatalf("hidden run decision=%q err=%v", action.Decision, err)
	}
	writeInbox(t, manager, "STOP\n")
	action, err = manager.CheckInbox("s1", false)
	if err != nil || !action.Stop {
		t.Fatalf("stop=%t err=%v", action.Stop, err)
	}
	cleared, _ := os.ReadFile(manager.InboxPath())
	if len(cleared) != 0 {
		t.Fatalf("INBOX was not cleared: %q", cleared)
	}
}

func TestInboxCapabilityTierAndCap(t *testing.T) {
	manager, cfg := testManager(t)
	writeInbox(t, manager, "ANSWER: session\n")
	action, err := manager.CheckInbox("s1", true)
	if err != nil || action.Decision != "" {
		t.Fatalf("off decision=%q err=%v", action.Decision, err)
	}
	cfg.OperatorFiles.AllowMailboxApprovals = true
	writeInbox(t, manager, "ANSWER: for this chat\n")
	action, err = manager.CheckInbox("s1", true)
	if err != nil || action.Decision != "session" {
		t.Fatalf("on decision=%q err=%v", action.Decision, err)
	}
	oversized := strings.Repeat("x", InboxMaxBytes+1)
	writeInbox(t, manager, oversized)
	if _, err := manager.CheckInbox("s1", false); err == nil {
		t.Fatal("oversized INBOX was accepted")
	}
	retained, _ := os.ReadFile(manager.InboxPath())
	if string(retained) != oversized {
		t.Fatal("oversized INBOX was changed")
	}
}

func TestOutboxRotatesAtTwoHundredLines(t *testing.T) {
	manager, _ := testManager(t)
	manager.now = func() time.Time { return time.Date(2026, 9, 8, 1, 2, 3, 4, time.UTC) }
	for index := 0; index < OutboxMaxLines+1; index++ {
		if err := manager.AppendOutbox("s1", "done: item"); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := lineCount(manager.OutboxPath()); err != nil || count != 1 {
		t.Fatalf("active lines=%d err=%v", count, err)
	}
	matches, _ := filepath.Glob(filepath.Join(manager.Root(), "OUTBOX-*.md"))
	if len(matches) != 1 {
		t.Fatalf("rotations=%v", matches)
	}
	if count, err := lineCount(matches[0]); err != nil || count != OutboxMaxLines {
		t.Fatalf("rotated lines=%d err=%v", count, err)
	}
}

func TestOutboxUsesHumanEventVocabulary(t *testing.T) {
	manager, _ := testManager(t)
	snapshot := &session.Snapshot{ID: "s1", Label: "Release check"}
	manager.HandleEvent(events.New(events.ModelUnreachable, "s1", "r1", map[string]any{}), snapshot)
	manager.HandleEvent(events.New(events.ApprovalRequired, "s1", "r1", map[string]any{}), snapshot)
	manager.HandleEvent(events.New(events.RunStopped, "s1", "r1", map[string]any{"reason": "done"}), snapshot)
	manager.HandleEvent(events.New(events.RunStopped, "s1", "r2", map[string]any{"reason": "safe"}), snapshot)
	data, err := os.ReadFile(manager.OutboxPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pause:", "needs you:", "done:", "stopped:"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("OUTBOX missing %q:\n%s", want, data)
		}
	}
}

func TestStateIsAtomicallyReplacedAtRunAndTurnBoundaries(t *testing.T) {
	manager, _ := testManager(t)
	now := time.Date(2026, 9, 8, 1, 2, 3, 0, time.UTC)
	manager.now = func() time.Time { return now }
	snapshot := &session.Snapshot{ID: "s1", Label: "Fix tests", Run: session.RunState{Turn: 2}}
	manager.HandleEvent(events.New(events.RunStarted, "s1", "r1", map[string]any{}), snapshot)
	now = now.Add(3 * time.Second)
	manager.HandleEvent(events.New(events.ModelRequest, "s1", "r1", map[string]any{"turn": 2}), snapshot)
	data, err := os.ReadFile(manager.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"status: running", "chat: Fix tests", "item: turn 2", "elapsed: 3s", "waiting: nothing"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("STATE missing %q:\n%s", want, data)
		}
	}
	if temporary, _ := filepath.Glob(filepath.Join(manager.Root(), ".STATE-*.tmp")); len(temporary) != 0 {
		t.Fatalf("temporary files remain: %v", temporary)
	}
}

func TestAdoptIsNonDestructiveByDefaultAndCleanupIsExplicit(t *testing.T) {
	manager, _ := testManager(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agent rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, removed, err := manager.Adopt(dir, false)
	if err != nil || len(removed) != 0 {
		t.Fatalf("path=%q removed=%v err=%v", path, removed, err)
	}
	for _, name := range []string{"AGENT_B.md", "AGENTS.md", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	dir = t.TempDir()
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+" rules\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, removed, err = manager.Adopt(dir, true)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
}

func TestRetentionDeletesOnlyExpiredTopLevelJSONL(t *testing.T) {
	manager, cfg := testManager(t)
	published := []events.Event{}
	manager.SetEventPublisher(func(event events.Event) { published = append(published, event) })
	cfg.OperatorFiles.LogRetentionDays = 30
	if err := os.MkdirAll(manager.logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	old := filepath.Join(manager.logDir, "old.jsonl")
	newer := filepath.Join(manager.logDir, "new.jsonl")
	evidence := filepath.Join(manager.logDir, "evidence", "old.jsonl")
	for _, path := range []string{old, newer, evidence} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Chtimes(old, now.Add(-31*24*time.Hour), now.Add(-31*24*time.Hour))
	_ = os.Chtimes(evidence, now.Add(-31*24*time.Hour), now.Add(-31*24*time.Hour))
	removed, err := manager.ApplyRetention()
	if err != nil || len(removed) != 1 || removed[0] != old {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	for _, path := range []string{newer, evidence} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained file %s: %v", path, err)
		}
	}
	if len(published) != 1 || published[0].Type != events.LogRetention {
		t.Fatalf("published=%+v", published)
	}
	data, _ := json.Marshal(published[0].Data)
	if !strings.Contains(string(data), `"days":30`) || !strings.Contains(string(data), `"files":["old.jsonl"]`) {
		t.Fatalf("retention event=%s", data)
	}
}

func TestRetentionRefusesARootThatIsNotAPlainDirectory(t *testing.T) {
	manager, cfg := testManager(t)
	cfg.OperatorFiles.LogRetentionDays = 30
	now := time.Date(2026, 9, 16, 0, 40, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	outside := filepath.Join(t.TempDir(), "node_modules")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "old.jsonl")
	if err := os.WriteFile(victim, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(victim, now.Add(-31*24*time.Hour), now.Add(-31*24*time.Hour))

	linked := filepath.Join(filepath.Dir(manager.logDir), "linked-logs")
	if err := os.Symlink(outside, linked); err != nil {
		if output, mklinkErr := exec.Command("cmd", "/c", "mklink", "/J", linked, outside).CombinedOutput(); mklinkErr != nil {
			t.Skipf("cannot create a link: %v / %v %s", err, mklinkErr, output)
		}
	}
	for _, root := range []string{linked, "relative-logs", filepath.VolumeName(outside) + string(filepath.Separator)} {
		manager.logDir = root
		removed, err := manager.ApplyRetention()
		if err == nil || len(removed) != 0 || !strings.Contains(err.Error(), "retention refused") {
			t.Fatalf("root %q: removed=%v err=%v", root, removed, err)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("retention through a linked root deleted %s: %v", victim, err)
	}

	// A link named *.jsonl inside a real log directory is never followed or removed.
	manager.logDir = filepath.Join(filepath.Dir(linked), "logs")
	link := filepath.Join(manager.logDir, "linked.jsonl")
	if err := os.Symlink(victim, link); err == nil {
		removed, err := manager.ApplyRetention()
		if err != nil || len(removed) != 0 {
			t.Fatalf("link candidate: removed=%v err=%v", removed, err)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("retention followed a link: %v", err)
	}
}

// Item 2py CHECK 2: the sweep removes what the old delete-time export wrote, and
// nothing else — a live chat in the store, its folder, and a file someone named
// like an export in a folder named like one all stay. One count line, no names.
func TestTheExportSweepRemovesOnlyOldExports2py(t *testing.T) {
	root := t.TempDir()
	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	export := "# Review build\n\n- chat: `s1`\n- agent: Local\n- folder: `C:/x`\n- closed: 2026-09-30T10:00:00Z\n\n## Transcript\n\n### You\n\nPLANTED words\n"
	write(filepath.Join(root, "chats", "repo-1a2b3c4d", "2026-09-30-Review-build.md"), export)
	write(filepath.Join(root, "chats", "repo-1a2b3c4d", "2026-09-30-Review-build-2.md"), export)
	kept := []string{
		filepath.Join(root, "chats", "s1.jsonl"),
		filepath.Join(root, "chats", "chat (3)", "chat.json"),
		filepath.Join(root, "chats", "notes-0badf00d", "2026-10-01-plan.md"),
	}
	for _, path := range kept {
		write(path, "his own words\n")
	}
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	removed, err := SweepChatExports([]string{root})
	if err != nil || removed != 2 {
		t.Fatalf("removed %d, %v", removed, err)
	}
	if line := output.String(); !strings.Contains(line, "chat export sweep: removed 2 file(s) from 1 profile(s)") || strings.Contains(line, "Review") || strings.Contains(line, "repo-") {
		t.Fatalf("log line = %q", line)
	}
	for _, path := range kept {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s did not survive: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "chats", "repo-1a2b3c4d")); !os.IsNotExist(err) {
		t.Errorf("the emptied export folder stayed: %v", err)
	}
	if again, _ := SweepChatExports([]string{root}); again != 0 {
		t.Errorf("a second start swept %d", again)
	}
}
