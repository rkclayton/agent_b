package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"harness/internal/events"
	"harness/internal/session"
)

// Item 2mt (a): a crash leaves a record, and the record names itself where the
// operator already looks. His crash at 03:10:50 on 2026-09-27 left neither.
func TestACrashWritesARecordAndNamesItInTheLauncherLog(t *testing.T) {
	root := t.TempDir()
	installCrashRecord(root, map[string]any{"tag": "v1.26.0", "commit": "abcdef1234"}, false)
	// What the surfaces would have noted while things were still fine.
	noteForCrash("window", map[string]any{"dragging": true, "x": 100, "y": 200})
	noteForCrash("journal", map[string]any{"session_id": "s34", "seq": 6805, "run_id": "r643"})

	var lines []string
	func() {
		// recoverCrash re-panics on purpose, so the test catches it the way the
		// process would: the record is written first and the fault still ends the
		// call. Swallowing it would be the bug.
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("recoverCrash swallowed the panic instead of writing the record and re-panicking")
			}
		}()
		defer recoverCrash("host window procedure", func(line string) { lines = append(lines, line) })
		panic("WM_MOVE during a stream")
	}()

	entries, err := os.ReadDir(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	var crashFile string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "crash-") && strings.HasSuffix(entry.Name(), ".json") {
			crashFile = filepath.Join(root, "logs", entry.Name())
		}
	}
	if crashFile == "" {
		t.Fatalf("no crash file under logs/: %v", entries)
	}
	body, err := os.ReadFile(crashFile)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatalf("the crash record is not readable JSON: %v", err)
	}
	if record["where"] != "host window procedure" {
		t.Errorf("where = %v", record["where"])
	}
	if got, _ := record["panic"].(string); got != "WM_MOVE during a stream" {
		t.Errorf("panic = %q", got)
	}
	// The stack is the point of the file.
	if stack, _ := record["stack"].(string); !strings.Contains(stack, "crash_record_test.go") {
		t.Errorf("the stack does not name the faulting frame: %q", stack)
	}
	build, _ := record["build"].(map[string]any)
	if build["tag"] != "v1.26.0" {
		t.Errorf("build = %v", build)
	}
	// The state the surfaces noted: the window and the journal position, which is
	// exactly what was missing from his crash.
	state, _ := record["state"].(map[string]any)
	journal, _ := state["journal"].(map[string]any)
	if journal["session_id"] != "s34" || journal["run_id"] != "r643" {
		t.Errorf("the record carries no journal position: %v", state)
	}
	if window, _ := state["window"].(map[string]any); window["dragging"] != true {
		t.Errorf("the record carries no window state: %v", state)
	}

	// And it is named where he already looks.
	if len(lines) != 1 {
		t.Fatalf("launcher lines: %v", lines)
	}
	if !strings.Contains(lines[0], filepath.Base(crashFile)) {
		t.Errorf("the launcher line does not name the crash file: %q", lines[0])
	}
	if !strings.Contains(lines[0], "crashed in host window procedure") {
		t.Errorf("the launcher line does not say where: %q", lines[0])
	}
}

// With no data root there is nothing to write to, and that must not become a
// second crash inside the crash path.
func TestTheCrashPathSurvivesHavingNowhereToWrite(t *testing.T) {
	installCrashRecord("", nil, false)
	defer installCrashRecord(t.TempDir(), nil, false)
	if path := crashRecord.writeCrashRecord("nowhere", "boom", []byte("stack")); path != "" {
		t.Fatalf("wrote %q with no data root", path)
	}
	crashLauncherLine("this must not panic")
}

func TestRuntimeFatalTraceIsReportedOnceAtNextStart2p5(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	trace := "fatal error: invalid pointer found on stack\n\ngoroutine 7 [copystack]:\nmain.(*hostWindow).windowProcedure(0x1, 0x2, 0x3, 0x998)\n\tC:/agent/host_window_windows.go:399 +0x4fa\n"
	if err := os.WriteFile(filepath.Join(logs, crashStderrName), []byte(trace), 0o600); err != nil {
		t.Fatal(err)
	}
	reportRuntimeFatalTrace(root)
	reportRuntimeFatalTrace(root)
	body, err := os.ReadFile(filepath.Join(logs, launcherLogName))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(body), "runtime fatal error: invalid pointer found on stack; top frame main.(*hostWindow).windowProcedure"); got != 1 {
		t.Fatalf("launcher report count = %d:\n%s", got, body)
	}
}

// Item 2mt (e): the item asked me to verify that a run cut by process death is
// already closed as aborted_mid_model on restart. IT WAS NOT. The projector takes
// run.status from run.started and run.stopped, so a journal that ends on a
// model.delta projects as still RUNNING — which is exactly the shape of the
// operator's chat s34: run r643 started, 6,544 deltas, then the drag killed the
// process and nothing closed it. These cases pin the close.
func TestARunCutByProcessDeathIsClosedOnRestore(t *testing.T) {
	live := session.RunState{Status: "running", RunID: "r643", Turn: 1, MaxTurns: 20, Partial: "half a sentence"}

	// s34's shape: the last thing recorded was the model streaming.
	midModel := closeRunCutByProcessDeath(live, []events.Event{
		{Type: events.RunStarted, RunID: "r643"},
		{Type: events.ModelRequest, RunID: "r643"},
		{Type: events.ModelDelta, RunID: "r643"},
	})
	if midModel.Status != "idle" {
		t.Errorf("status = %q, a chat that claims to be thinking forever is the defect", midModel.Status)
	}
	if midModel.LastStopReason != "aborted_mid_model" {
		t.Errorf("reason = %q", midModel.LastStopReason)
	}
	if midModel.LastRunID != "r643" || midModel.RunID != "" {
		t.Errorf("the run id did not move to last_run_id: %+v", midModel)
	}
	if midModel.Partial != "" || midModel.Turn != 0 {
		t.Errorf("a closed run kept in-flight state: %+v", midModel)
	}
	// The turn ceiling is configuration, not run state, and survives.
	if midModel.MaxTurns != 20 {
		t.Errorf("max turns = %d", midModel.MaxTurns)
	}

	// Cut while a tool was running, and cut with neither recorded.
	midTool := closeRunCutByProcessDeath(live, []events.Event{{Type: events.ModelDelta}, {Type: events.ToolCallEvent}})
	if midTool.LastStopReason != "aborted_mid_tool" {
		t.Errorf("tool reason = %q", midTool.LastStopReason)
	}
	bare := closeRunCutByProcessDeath(live, []events.Event{{Type: events.RunStarted}})
	if bare.LastStopReason != "aborted_mid_run" {
		t.Errorf("bare reason = %q", bare.LastStopReason)
	}

	// A run that really did finish is left exactly as the journal says.
	done := session.RunState{Status: "idle", LastStopReason: "done", LastRunID: "r642"}
	if got := closeRunCutByProcessDeath(done, []events.Event{{Type: events.ModelDelta}}); !reflect.DeepEqual(got, done) {
		t.Errorf("a finished run was rewritten: %+v", got)
	}
}
