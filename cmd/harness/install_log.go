package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Item 2gv (v1.2.5): an install that fails says so.
//
// The operator double-clicked Agent_b-setup.exe on 2026-09-21 and nothing
// happened - no window, no message, nothing written. A console program started
// from Explorer has nowhere to print: its stderr goes to a console that is
// closed the moment it exits. So every complaint the install mode made before
// it had a window was addressed to nobody.
//
// The fix is an order, not a feature: the LOG FILE IS THE FIRST THING THE
// PROCESS DOES. Before elevation, before the marker, before a single check.
// After that every exit is logged with its reason, and a failure early enough
// that there is no Setup page to show it puts up a message box naming the log.

type installLog struct {
	path string
	file *os.File
	// quiet is the suite's path. A message box is MODAL: shown from a test or
	// from the installer suite it would block that run until somebody clicked
	// it, on the operator's own desktop. The box is for the operator who
	// double-clicked something and got nothing; it is never for a test.
	quiet bool
}

// openInstallLog creates the log before anything else happens. If the data root
// cannot be written - which is itself one of the failures this has to report -
// it falls back to the temporary directory, because a log somewhere is worth
// more than a log nowhere.
func openInstallLog(dataRoot string, quiet bool) *installLog {
	stamp := time.Now().Format("20060102-150405")
	for _, dir := range []string{filepath.Join(dataRoot, "logs"), os.TempDir()} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		path := filepath.Join(dir, "installer-"+stamp+".log")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			continue
		}
		log := &installLog{path: path, file: file, quiet: quiet}
		log.printf("install: log opened at %s", path)
		return log
	}
	// Nowhere is writable. Everything below still works; it just has no file.
	return &installLog{quiet: quiet}
}

func (l *installLog) printf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	stamped := time.Now().Format("2006-01-02T15:04:05.000Z07:00") + " " + line
	if l != nil && l.file != nil {
		fmt.Fprintln(l.file, stamped)
		_ = l.file.Sync()
	}
	// A console, when there is one. From Explorer there is not, which is the
	// whole reason the file above exists.
	fmt.Fprintln(os.Stderr, line)
}

// fail logs the reason, shows it where the operator will see it, and returns
// the exit code. Every exit path after the log is opened goes through here, so
// "it just closed" cannot happen again.
func (l *installLog) fail(format string, args ...any) int {
	reason := fmt.Sprintf(format, args...)
	l.printf("install FAILED: %s", reason)
	if !l.quiet {
		showInstallFailure("Agent_b install failed", reason+"\n\nLog: "+l.location())
	}
	return 1
}

func (l *installLog) location() string {
	if l == nil || l.path == "" {
		return "(no log file could be created)"
	}
	return l.path
}

func (l *installLog) close() {
	if l != nil && l.file != nil {
		_ = l.file.Close()
	}
}
