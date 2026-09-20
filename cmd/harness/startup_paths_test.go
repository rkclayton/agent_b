package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Item 2gw (v1.2.5): the binary's own files live beside the binary. Resolving
// them against the WORKING DIRECTORY meant the exe started from C:\ or from
// Explorer looked for its own web and prompts in C:\ and found nothing; the
// launcher only worked because it set the working directory first.
func TestApplicationRootIsTheExecutablesDirectoryNotTheWorkingDirectory(t *testing.T) {
	elsewhere := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(elsewhere); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(previous)

	paths, err := resolveStartupPaths("", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Dir(executable)
	if resolved, linkErr := filepath.EvalSymlinks(executable); linkErr == nil {
		want = filepath.Dir(resolved)
	}
	if paths.Application != want {
		t.Fatalf("application root=%q, want the executable's directory %q", paths.Application, want)
	}
	if paths.Application == elsewhere {
		t.Fatal("the application root followed the working directory")
	}
}

// An explicit --app-root still wins: the suite and the install mode both pass it.
func TestApplicationRootHonoursItsOverride(t *testing.T) {
	override := t.TempDir()
	paths, err := resolveStartupPaths("", override, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if paths.Application != override {
		t.Fatalf("application root=%q, want %q", paths.Application, override)
	}
}
