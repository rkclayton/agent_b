package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"harness/internal/events"
	"harness/internal/session"
)

// Item 2mt (a): A CRASH LEAVES A RECORD.
//
// The operator, 2026-09-27 03:11: "i dragged the window while he was thinking, it
// crashed." What his data root actually holds: chat s34's journal stops on a
// `model.delta` at 08:10:50.281Z, seq 6805, with run r643 started at 08:09:04 and
// never reaching `model.response` or `run.stopped` — 6,544 deltas in and then
// nothing. The whole process died, server and host window together, and NOTHING
// SAID SO.
//
// Recovery runs at every boundary where Windows calls back into Go.
const crashStderrName = "crash-stderr.log"

type crashContext struct {
	mu       sync.Mutex
	dataRoot string
	build    map[string]any
	// state is whatever the surfaces want the next crash to carry: the window's
	// geometry, the newest journal position, the streaming session. It is small,
	// last-write-wins, and read only when something has already gone wrong.
	state map[string]any
}

var crashRecord = &crashContext{state: map[string]any{}}

// installCrashRecord wires the record up once, at startup, before anything that
// can fault. It never fails the launch: a process that cannot write a crash file
// still has to run.
// keepTraceback is passed, not inferred. An earlier draft guessed from "is a
// console attached", which is false under `go test` too — so it redirected the
// TEST binary's stderr into a temp file and held the handle open. Only the
// detached host-window launch has nowhere for stderr to go, so only it asks.
func installCrashRecord(dataRoot string, build map[string]any, keepTraceback bool) {
	reportRuntimeFatalTrace(dataRoot)
	crashRecord.mu.Lock()
	crashRecord.dataRoot = dataRoot
	crashRecord.build = build
	crashRecord.mu.Unlock()
	if dataRoot == "" {
		return
	}
	_ = os.MkdirAll(filepath.Join(dataRoot, "logs"), 0o700)
	if keepTraceback {
		keepRuntimeTraceback(filepath.Join(dataRoot, "logs", crashStderrName))
	}
}

func reportRuntimeFatalTrace(dataRoot string) {
	path := filepath.Join(dataRoot, "logs", crashStderrName)
	file, err := os.Open(path)
	if err != nil {
		return
	}
	body, err := io.ReadAll(io.LimitReader(file, 256<<10))
	file.Close()
	if err != nil {
		return
	}
	lines := strings.Split(string(body), "\n")
	fatal, frame := "", ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if fatal == "" && strings.HasPrefix(line, "fatal error:") {
			fatal = line
			continue
		}
		if fatal != "" && frame == "" && strings.Contains(line, "(") && !strings.HasPrefix(line, "runtime.") {
			frame = line[:strings.LastIndex(line, "(")]
			break
		}
	}
	if fatal == "" || frame == "" {
		return
	}
	appendLauncherMessage(dataRoot, "Agent_b runtime "+printable(fatal)+"; top frame "+printable(frame))
	_ = os.WriteFile(path, nil, 0o600)
}

// note records something the next crash should carry. Callers are surfaces, not
// error paths: this is called while things are fine.
func noteForCrash(key string, value any) {
	crashRecord.mu.Lock()
	defer crashRecord.mu.Unlock()
	crashRecord.state[key] = value
}

// writeCrashRecord writes one crash file and returns its path. `where` names the
// boundary that caught it, so a reader knows whether Windows called us or we
// called ourselves.
func (c *crashContext) writeCrashRecord(where string, recovered any, stack []byte) string {
	return c.writeCrashRecordWithPCs(where, recovered, stack, nil)
}

func (c *crashContext) writeCrashRecordWithPCs(where string, recovered any, stack []byte, pcs []uint64) string {
	c.mu.Lock()
	dataRoot, build := c.dataRoot, c.build
	state := make(map[string]any, len(c.state))
	for key, value := range c.state {
		state[key] = value
	}
	c.mu.Unlock()
	if dataRoot == "" {
		return ""
	}
	stamp := time.Now().Format("20060102-150405.000")
	path := filepath.Join(dataRoot, "logs", fmt.Sprintf("crash-%s.json", strings.ReplaceAll(stamp, ".", "-")))
	keys := make([]string, 0, len(state))
	for key := range state {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	record := map[string]any{
		"at":          time.Now().UTC().Format(time.RFC3339Nano),
		"where":       where,
		"panic":       fmt.Sprintf("%v", recovered),
		"stack":       string(stack),
		"build":       build,
		"pid":         os.Getpid(),
		"goos":        runtime.GOOS,
		"go":          runtime.Version(),
		"routines":    runtime.NumGoroutine(),
		"state":       state,
		"state_keys":  keys,
		"stack_pcs":   pcs,
		"module_base": executableModuleBase(),
	}
	if executable, err := os.Executable(); err == nil {
		record["executable"] = filepath.Base(executable)
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return ""
	}
	if os.WriteFile(path, append(body, '\n'), 0o600) != nil {
		return ""
	}
	return path
}

// recoverCrash is the boundary guard. Deferred at every point Windows calls into
// Go, it writes the record, names it in launcher-errors.log so the operator finds
// it where he already looks, and then RE-PANICS: swallowing a fault inside a
// window procedure would leave the process alive and lying about its state, and
// the item asks for a record, not for the crash to be hidden.
func recoverCrash(where string, lifetimeLog func(string)) {
	recovered := recover()
	if recovered == nil {
		return
	}
	stack := debug.Stack()
	path := crashRecord.writeCrashRecordWithPCs(where, recovered, stack, crashProgramCounters())
	if lifetimeLog != nil {
		if path != "" {
			lifetimeLog(fmt.Sprintf("Agent_b PID %d crashed in %s: %v — the stack, the build and the last known window and journal state are in %s", os.Getpid(), where, recovered, path))
		} else {
			lifetimeLog(fmt.Sprintf("Agent_b PID %d crashed in %s: %v — no crash file could be written", os.Getpid(), where, recovered))
		}
	}
	panic(recovered)
}

func crashProgramCounters() []uint64 {
	callers := make([]uintptr, 64)
	n := runtime.Callers(3, callers)
	frames, out := runtime.CallersFrames(callers[:n]), make([]uint64, 0, 50)
	for len(out) < 51 {
		frame, more := frames.Next()
		if !strings.HasPrefix(frame.Function, "runtime.") && !strings.Contains(frame.Function, "recoverCrash") && !strings.Contains(frame.Function, "crashProgramCounters") {
			out = append(out, uint64(frame.PC))
		}
		if !more {
			break
		}
	}
	return out
}

type pendingCrashReport struct {
	Path, At string
	Data     map[string]any
}

func pendingCrashReports(dataRoot string) []pendingCrashReport {
	dir := filepath.Join(dataRoot, "logs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var reports []pendingCrashReport
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "crash-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if _, err := os.Stat(path + ".telemetry-reported"); err == nil {
			continue
		}
		var record struct {
			At, Where, Executable string
			StackPCs              []uint64       `json:"stack_pcs"`
			ModuleBase            uint64         `json:"module_base"`
			Build                 map[string]any `json:"build"`
		}
		body, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(body, &record) != nil || len(record.StackPCs) == 0 {
			continue
		}
		digest, _ := record.Build["executable_sha256"].(string)
		if len(digest) < 32 || record.Executable == "" {
			continue
		}
		frames := make([]map[string]any, 0, len(record.StackPCs))
		for index, pc := range record.StackPCs {
			if index == 50 {
				break
			}
			if pc >= record.ModuleBase {
				frames = append(frames, map[string]any{"binary": 0, "offset": pc - record.ModuleBase, "address": pc})
			}
		}
		if len(frames) == 0 {
			continue
		}
		uuid := fmt.Sprintf("%s-%s-%s-%s-%s", digest[:8], digest[8:12], digest[12:16], digest[16:20], digest[20:32])
		tree := map[string]any{"binaries": []any{map[string]any{"uuid": strings.ToLower(uuid), "name": filepath.Base(record.Executable), "text_offset": 0}}, "threads": []any{map[string]any{"frames": frames}}, "exception_type": 0, "signal": 0, "termination_reason": "go.panic", "truncated": len(record.StackPCs) > 50}
		reports = append(reports, pendingCrashReport{Path: path, At: record.At, Data: map[string]any{"where": crashWhere(record.Where), "class": "crash", "stack_tree": tree}})
	}
	return reports
}

func crashWhere(value string) string {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), " ", "_"))
	if value == "" {
		return "runtime"
	}
	return value
}
func markCrashReported(path string) error {
	return os.WriteFile(path+".telemetry-reported", nil, 0o600)
}

// crashLauncherLine puts the crash where the operator already looks. The data
// root is the one installCrashRecord was given, so no surface has to carry a copy
// of it just to be able to report its own failure.
func crashLauncherLine(line string) {
	crashRecord.mu.Lock()
	dataRoot := crashRecord.dataRoot
	crashRecord.mu.Unlock()
	if dataRoot == "" {
		return
	}
	appendLauncherMessage(dataRoot, line)
}

// liveRunStatuses are the statuses that mean a run was in flight. A journal that
// ends while one of them is projected ended because the PROCESS ended: a run that
// finishes writes `run.stopped`, and one that is stopped writes it too.
var liveRunStatuses = map[string]bool{"running": true, "queued": true, "stopping": true, "paused": true}

// closeRunCutByProcessDeath turns an open run into a stopped one at restore, with
// the reason INTERFACES.md already defines for it. Item 2mt (e).
//
// The reason distinguishes where it was cut, because the transcript should say:
// mid-model if the last thing the journal recorded was the model streaming, and
// mid-run otherwise. Those are two of the three `last_stop_reason` values
// INTERFACES.md lists for exactly this ("`aborted_mid_model`, `aborted_mid_tool`
// and `aborted_mid_run` from `done`"), and no new vocabulary is invented.
func closeRunCutByProcessDeath(run session.RunState, timeline []events.Event) session.RunState {
	if !liveRunStatuses[run.Status] {
		return run
	}
	reason := "aborted_mid_run"
	for index := len(timeline) - 1; index >= 0; index-- {
		switch timeline[index].Type {
		case events.ModelDelta, events.ModelRequest:
			reason = "aborted_mid_model"
		case events.ToolCallEvent:
			reason = "aborted_mid_tool"
		default:
			continue
		}
		break
	}
	run.LastRunID = run.RunID
	run.Status = "idle"
	run.RunID = ""
	run.Turn = 0
	run.QueuePosition = 0
	run.Partial = ""
	run.LastStopReason = reason
	run.LastStopDetail = "the process ended while this run was in flight"
	return run
}
