//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Item 2x0: an installed script still held open for a moment after its process
// stopped no longer fails the install half-way; the swap waits for it, and
// nothing is left beside the folder.
func TestReplaceDirectoryWaitsForAHeldFile2x0(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "scripts")
	for path, text := range map[string]string{filepath.Join(source, "launch.ps1"): "new", filepath.Join(destination, "launch.ps1"): "old"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	held, err := os.Open(filepath.Join(destination, "launch.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(time.Second); held.Close() }()
	if err := replaceDirectory(source, destination); err != nil {
		t.Fatalf("replace while held: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(destination, "launch.ps1")); string(got) != "new" {
		t.Fatalf("installed = %q", got)
	}
	for _, leftover := range []string{destination + ".installing", destination + ".previous"} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Fatalf("%s left behind (%v)", leftover, err)
		}
	}
}

// Item 2x0: a shortcut written from a goroutine that the scheduler moves between
// threads still finds COM initialized; unpinned, an install over the top failed
// with CO_E_NOTINITIALIZED.
func TestShellLinksFromManyGoroutinesAllSucceed2x0(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.exe")
	if err := os.WriteFile(target, []byte("stand-in"), 0o600); err != nil {
		t.Fatal(err)
	}
	failures := make(chan error, 64)
	done := make(chan struct{})
	for index := 0; index < 64; index++ {
		go func(index int) {
			defer func() { done <- struct{}{} }()
			spec := shortcutSpec{Path: filepath.Join(root, fmt.Sprintf("link-%d.lnk", index)), Target: target, WorkingDirectory: root, Description: "stand-in"}
			if err := writeShellLink(spec); err != nil {
				failures <- err
			}
		}(index)
	}
	for index := 0; index < 64; index++ {
		<-done
	}
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
}
