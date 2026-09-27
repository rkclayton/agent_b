package main

import (
	"encoding/json"
	"fmt"
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

// Item 2mt (a): A CRASH LEAVES A RECORD, and this lands before the fix because
// the next crash has to be readable.
//
// The operator, 2026-09-27 03:11: "i dragged the window while he was thinking, it
// crashed." What his data root actually holds: chat s34's journal stops on a
// `model.delta` at 08:10:50.281Z, seq 6805, with run r643 started at 08:09:04 and
// never reaching `model.response` or `run.stopped` — 6,544 deltas in and then
// nothing. The whole process died, server and host window together, and NOTHING
// SAID SO.
//
// Two separate silences, and only one of them was already covered:
//
//  1. THAT it died. `lifetime.begin` already notices a previous marker whose PID
//     is gone and appends "ended without recording a reason" — but only on the
//     NEXT START, and he has not started it since, which is why his
//     launcher-errors.log still ends at 03:03:51. That mechanism is kept and is
//     not duplicated here.
//  2. WHERE it died. Nothing wrote a stack, a build, a window state or a journal
//     position — and a Go panic prints its traceback to STDERR, which the
//     launcher detaches and does not persist (AGENTS.md says so in as many
//     words). So the one artefact that would name the faulting line was thrown
//     away by design.
//
// This file closes the second. It does two things a panic cannot do for itself:
// it keeps the runtime's own traceback by giving stderr somewhere to land when
// there is no console to print to, and it writes a structured record from a
// recover() at every boundary where Windows calls back into Go.
//
// There is no recover() anywhere in the window procedure today, and a panic in a
// syscall callback takes the process with it, which is consistent with everything
// above.

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
		"at":         time.Now().UTC().Format(time.RFC3339Nano),
		"where":      where,
		"panic":      fmt.Sprintf("%v", recovered),
		"stack":      string(stack),
		"build":      build,
		"pid":        os.Getpid(),
		"goos":       runtime.GOOS,
		"go":         runtime.Version(),
		"routines":   runtime.NumGoroutine(),
		"state":      state,
		"state_keys": keys,
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
	path := crashRecord.writeCrashRecord(where, recovered, stack)
	if lifetimeLog != nil {
		if path != "" {
			lifetimeLog(fmt.Sprintf("Agent_b PID %d crashed in %s: %v — the stack, the build and the last known window and journal state are in %s", os.Getpid(), where, recovered, path))
		} else {
			lifetimeLog(fmt.Sprintf("Agent_b PID %d crashed in %s: %v — no crash file could be written", os.Getpid(), where, recovered))
		}
	}
	panic(recovered)
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
