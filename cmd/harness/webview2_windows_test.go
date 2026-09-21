//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repositoryFile walks up from the test's working directory to the repository
// root, so the test finds the shipped loader without a hard-coded path.
func repositoryFile(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("%s was not found above %s", name, dir)
		}
		dir = parent
	}
}

// Item 2hc (v1.3.0/W2): the loader is taken from the executable's own directory
// and nowhere else, and it answers with the installed runtime version.
//
// The test copies the shipped DLL beside the TEST binary, which is what
// os.Executable() reports, so it exercises the real path rather than a stub. It
// also proves the honest-absence branch first: with no loader beside the
// executable the probe must decline with a reason, never panic and never fall
// back to the DLL search path, because a bare LoadLibrary would happily pick up
// somebody else's WebView2Loader.dll from the working directory or PATH.
func TestTheWebView2LoaderIsTakenFromTheExecutableDirectory(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	beside := filepath.Join(filepath.Dir(executable), webview2LoaderName)

	path, err := webview2LoaderPath()
	if err != nil {
		t.Fatalf("loader path: %v", err)
	}
	if !strings.EqualFold(path, beside) {
		t.Fatalf("loader path is %q, expected the executable's own directory %q", path, beside)
	}

	if _, err := os.Stat(beside); err == nil {
		t.Skip("a loader is already beside the test binary; the absence branch cannot be observed here")
	}
	// Absence, before anything is copied: a reason, not a panic.
	if _, err := hostWindowAvailable(); err == nil {
		t.Fatal("the probe reported a host window with no loader beside the executable")
	} else if !strings.Contains(err.Error(), webview2LoaderName) {
		t.Fatalf("the absence reason does not name the loader: %v", err)
	}
}

// With the shipped loader in place the probe must name the installed runtime.
// This is the half that proves the binding works, not merely that it fails
// politely: a wrong export name or calling convention shows up here.
func TestTheWebView2ProbeNamesTheInstalledRuntime(t *testing.T) {
	source := repositoryFile(t, webview2LoaderName)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	destination := filepath.Join(filepath.Dir(executable), webview2LoaderName)
	payload, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("reading the shipped loader: %v", err)
	}
	if err := os.WriteFile(destination, payload, 0o644); err != nil {
		t.Skipf("the test binary's directory is not writable: %v", err)
	}
	t.Cleanup(func() { os.Remove(destination) })

	version, err := hostWindowAvailable()
	if err != nil {
		t.Fatalf("the probe declined with the shipped loader in place: %v", err)
	}
	if version == "" || !strings.Contains(version, ".") {
		t.Fatalf("the runtime version is not a version: %q", version)
	}
	t.Logf("WebView2 runtime %s, through %s", version, destination)
}
