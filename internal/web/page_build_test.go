package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/buildinfo"
	"harness/internal/config"
	"harness/internal/events"
)

// Item 2ev: the document is never stored, carries the build that served it,
// and versions its assets; assets are revalidated on every use.
func TestDocumentIsNoStoreAndCarriesTheServingBuild(t *testing.T) {
	webDir := t.TempDir()
	page := "<!doctype html>\n<html>\n<head>\n  <link rel=\"stylesheet\" href=\"/static/css/chat.css\">\n</head>\n<body><script type=\"module\" src=\"/static/js/build-check.js\"></script></body>\n</html>\n"
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte(page), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(webDir, "css"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "css", "chat.css"), []byte("body{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(t.TempDir())
	server := New(&cfg, filepath.Join(t.TempDir(), "harness.json"), webDir, RuntimeRoots{Application: t.TempDir()}, events.NewBus())

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/chat", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("document Cache-Control=%q", got)
	}
	build := buildinfo.Current().ExecutableSHA256
	if build == "" {
		t.Fatal("test binary reports no executable hash")
	}
	body := response.Body.String()
	if !strings.Contains(body, `<meta name="agentb-build" content="`+build+`">`) {
		t.Fatalf("document does not carry the serving build %s:\n%s", build, body)
	}
	for _, asset := range []string{`href="/static/~` + build[:12] + `/css/chat.css"`, `src="/static/~` + build[:12] + `/js/build-check.js"`} {
		if !strings.Contains(body, asset) {
			t.Fatalf("document is missing versioned asset %s:\n%s", asset, body)
		}
	}

	asset := httptest.NewRecorder()
	server.Handler().ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/static/~"+build[:12]+"/css/chat.css", nil))
	if asset.Code != http.StatusOK || asset.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("asset status=%d Cache-Control=%q", asset.Code, asset.Header().Get("Cache-Control"))
	}
}

func TestStampDocumentLeavesOtherReferencesAlone(t *testing.T) {
	got := string(stampDocument([]byte(`<head><a href="https://example.com/static/x.js"></a><img src="/static/a.svg?x=1"><img src="/static/b.svg"></head>`), "0123456789abcdef"))
	if !strings.Contains(got, `href="https://example.com/static/x.js"`) || !strings.Contains(got, `src="/static/~0123456789ab/a.svg?x=1"`) || !strings.Contains(got, `src="/static/~0123456789ab/b.svg"`) {
		t.Fatalf("unexpected stamping: %s", got)
	}
	if !strings.HasPrefix(got, "<head>\n  <meta name=\"agentb-build\" content=\"0123456789abcdef\">") {
		t.Fatalf("meta not first in head: %s", got)
	}
}
