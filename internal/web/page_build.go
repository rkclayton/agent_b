package web

import (
	"net/http"
	"regexp"
	"strings"

	"harness/internal/buildinfo"
)

// Item 2ev: a browser must never run one build's page against another build's
// server. Documents are never stored, every asset they reference carries the
// build id, assets are revalidated on every use, and the page carries the build
// id it was served by so the shell can compare it with /api/state.

var staticReference = regexp.MustCompile(`((?:src|href)=")(/static/[^"?#]+)(")`)

// pageBuildID is the executable's SHA-256: it changes with every build, dirty or
// not, and it is what /api/state reports as build.executable_sha256.
func pageBuildID() string {
	info := buildinfo.Current()
	if info.ExecutableSHA256 != "" {
		return info.ExecutableSHA256
	}
	return info.Commit
}

// stampDocument adds the build id meta element after <head> and appends the
// build id to every /static/ reference in the document.
func stampDocument(document []byte, build string) []byte {
	text := string(document)
	version := build
	if len(version) > 12 {
		version = version[:12]
	}
	text = staticReference.ReplaceAllString(text, "${1}${2}?v="+version+"${3}")
	meta := `<meta name="agentb-build" content="` + build + `">`
	if index := strings.Index(text, "<head>"); index >= 0 {
		insert := index + len("<head>")
		text = text[:insert] + "\n  " + meta + text[insert:]
	}
	return []byte(text)
}

func revalidateStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}
