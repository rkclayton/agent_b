package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Item 2gv (v1.2.5): the log is the first thing the install mode does, and
// every exit after it is logged with its reason. A failure before the log is a
// failure nobody can read - which is what a double-clicked setup produced.
func TestInstallLogIsWrittenBeforeAnythingElse(t *testing.T) {
	root := t.TempDir()
	// No source directory holds the installer script, so this fails at the
	// earliest check there is - and must still leave a log saying so.
	code := runInstall(installOptions{quiet: true, dataRoot: root, sourceDir: filepath.Join(root, "nowhere")}, nil)
	if code != 1 {
		t.Fatalf("exit=%d", code)
	}
	logs, err := filepath.Glob(filepath.Join(root, "logs", "installer-*.log"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs=%v err=%v", logs, err)
	}
	content, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{"log opened at", "install: starting", "install FAILED:", "is missing; run this from the candidate folder"} {
		if !strings.Contains(text, want) {
			t.Fatalf("log does not say %q:\n%s", want, text)
		}
	}
}

// When the data root cannot be written, the log still lands somewhere: a log
// somewhere is worth more than a log nowhere.
func TestInstallLogFallsBackWhenTheDataRootIsUnwritable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := openInstallLog(file, true)
	defer log.close()
	if log.path == "" {
		t.Fatal("no log was opened at all")
	}
	if strings.HasPrefix(log.path, file) {
		t.Fatalf("the log went under the unwritable root: %s", log.path)
	}
}
