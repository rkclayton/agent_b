package config

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// The bare word is reserved for the operator-owned namespace introduced after
// endpoint terminology was made unambiguous. Platform tokens such as
// USERPROFILE and PowerShell's NoProfile are intentionally not bare words and
// therefore need no exception.
func TestEndpointVocabularyReservesProfileWord(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate vocabulary test")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	reserved := regexp.MustCompile(`(?i)\bpro` + `files?\b`)
	for _, relative := range []string{"internal", filepath.Join("web", "js"), "scripts", "docs"} {
		root := filepath.Join(repository, relative)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if match := reserved.Find(data); match != nil {
				location, _ := filepath.Rel(repository, path)
				return &reservedVocabularyError{path: location, word: string(match)}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

type reservedVocabularyError struct {
	path string
	word string
}

func (err *reservedVocabularyError) Error() string {
	return strings.Join([]string{err.path, "contains reserved endpoint vocabulary", err.word}, ": ")
}
