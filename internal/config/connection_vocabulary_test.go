package config

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Endpoint vocabulary must remain unambiguous now that profile names belong to
// operators. Legacy endpoint-shaped names are read only through deliberately
// assembled compatibility aliases, never reintroduced as identifiers or UI.
func TestEndpointVocabularyDoesNotReuseOperatorProfileNames(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate vocabulary test")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	reserved := regexp.MustCompile(`(?i)\b(model_?pro` + `files?|pro` + `file_id|b_pro` + `file|main_pro` + `file)\b`)
	for _, relative := range []string{"internal", filepath.Join("web", "js"), "scripts", "docs"} {
		root := filepath.Join(repository, relative)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			// Checked-in legacy journals prove that the centralized projector
			// aliases continue to read the pre-connection vocabulary forever.
			if strings.Contains(filepath.ToSlash(path), "/internal/projection/testdata/") {
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
