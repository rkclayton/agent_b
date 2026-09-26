package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Item 2la. The operator: "the agent keeps running python and it prompts me in
// windows 'what do you want to run this program with' it just says python … hes
// just cycling." That dialog is a Microsoft Store app-execution alias answering a
// bare `python` and blocking on his desktop, so the call returned neither success
// nor a clean failure. A tool never waits on a desktop dialog.
func TestAnAliasStubIsRefusedBeforeLaunch2la(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(root, "WindowsApps")
	if err := os.MkdirAll(alias, 0o700); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(alias, "python.exe")
	if err := os.WriteFile(stub, []byte("not really an interpreter"), 0o700); err != nil {
		t.Fatal(err)
	}
	reason := unusableInterpreter(stub)
	if !strings.Contains(reason, "app-execution alias") || !strings.Contains(reason, "dialog") {
		t.Fatalf("an alias stub was not refused for opening a dialog: %q", reason)
	}
	if !isAppExecutionAlias(stub) {
		t.Fatal("the alias folder was not recognised")
	}
	if isAppExecutionAlias(filepath.Join(root, "Programs", "Python", "python.exe")) {
		t.Fatal("an ordinary install was mistaken for an alias")
	}
}

// The other face of the same problem on the operator's host: a ZERO-BYTE
// C:\Windows\System32\python with no extension, which produced
// `fork/exec …: The directory name is invalid` rather than a dialog.
func TestAnEmptyFileIsNotAnInterpreter2la(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "python")
	if err := os.WriteFile(empty, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	if reason := unusableInterpreter(empty); !strings.Contains(reason, "empty file") {
		t.Fatalf("a zero-byte file was accepted as an interpreter: %q", reason)
	}
	real := filepath.Join(root, "python-real")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if reason := unusableInterpreter(real); reason != "" {
		t.Fatalf("a real file was refused: %q", reason)
	}
	if reason := unusableInterpreter(root); !strings.Contains(reason, "directory") {
		t.Fatalf("a directory was accepted: %q", reason)
	}
}

// (a) and (c): a language with no usable interpreter is refused before launch,
// with a reason the operator can act on, rather than attempted.
func TestAMissingInterpreterIsRefusedWithWhatToInstall2la(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	resolved, reason := usableInterpreter("definitely-not-installed")
	if resolved != "" {
		t.Fatalf("a missing interpreter resolved to %q", resolved)
	}
	for _, want := range []string{"not installed", "PATH", "powershell"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("the refusal does not say %q: %s", want, reason)
		}
	}
}
