package attachment

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePDF(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.pdf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Item 2fj: the walk's walk-doc.pdf — a hand-written one-page PDF with no
// cross-reference table and a wrong stream length — is read locally.
func TestTheWalksPDFTextLayerIsReadLocally(t *testing.T) {
	text, err := ExtractPDFText(filepath.Join("testdata", "walk-doc.pdf"), 1<<20)
	if err != nil || !strings.Contains(string(text), "Walk PDF says orange 271") || !strings.Contains(string(text), "## Page 1") {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestAPDFWithoutTextIsNamedForOCR(t *testing.T) {
	path := writePDF(t, "%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 300 144]/Contents 4 0 R>>endobj\n4 0 obj<</Length 11>>stream\n0 0 m 1 1 l\nendstream endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n")
	if _, err := ExtractPDFText(path, 1<<20); !errors.Is(err, ErrNoTextLayer) {
		t.Fatalf("a page with no text must be ErrNoTextLayer: %v", err)
	}
}

func TestAnUndecodableStreamIsAnErrorNotAPanic(t *testing.T) {
	path := writePDF(t, "%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n3 0 obj<</Type/Page/Parent 2 0 R/Contents 4 0 R>>endobj\n4 0 obj<</Length 4/Filter/JBIG2Decode>>stream\nabcd\nendstream endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n")
	if _, err := ExtractPDFText(path, 1<<20); err == nil {
		t.Fatal("an undecodable stream must be an error")
	}
}

func TestLongPDFTextStopsAtTheLimitAndSaysSo(t *testing.T) {
	text, err := ExtractPDFText(filepath.Join("testdata", "walk-doc.pdf"), 10)
	if err != nil || !strings.Contains(string(text), "text truncated at 10 bytes") {
		t.Fatalf("text=%q err=%v", text, err)
	}
}
