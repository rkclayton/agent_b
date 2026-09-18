package web

import (
	"net/http"
	"regexp"
	"strings"

	"harness/internal/buildinfo"
)

// Item 2ev: a browser must never run one build's page against another build's
// server. Documents are never stored and carry the build id they were served
// by, so the shell can compare it with /api/state. Every /static/ reference in a
// document carries the build id as a path segment, so the relative module
// imports inside those files inherit it and no asset of an earlier build is
// reused from the browser cache; assets are also revalidated on every use.

var staticReference = regexp.MustCompile(`((?:src|href)=")/static/([^"]+)(")`)

var versionedStatic = regexp.MustCompile(`^/static/~[0-9a-f]{1,64}/`)

var hexVersion = regexp.MustCompile(`^[0-9a-f]+$`)

// pageBuildID is the executable's SHA-256: it changes with every build, dirty or
// not, and it is what /api/state reports as build.executable_sha256.
func pageBuildID() string {
	info := buildinfo.Current()
	if info.ExecutableSHA256 != "" {
		return info.ExecutableSHA256
	}
	return info.Commit
}

func staticVersion(build string) string {
	version := strings.ToLower(build)
	if len(version) > 12 {
		version = version[:12]
	}
	if !hexVersion.MatchString(version) {
		return "0"
	}
	return version
}

// stampDocument adds the build id meta element after <head> and moves every
// /static/ reference in the document under /static/~<version>/.
func stampDocument(document []byte, build string) []byte {
	text := string(document)
	text = staticReference.ReplaceAllString(text, "${1}/static/~"+staticVersion(build)+"/${2}${3}")
	meta := `<meta name="agentb-build" content="` + build + `">`
	if index := strings.Index(text, "<head>"); index >= 0 {
		insert := index + len("<head>")
		text = text[:insert] + "\n  " + meta + text[insert:]
	}
	return []byte(text)
}

// revalidateStatic strips the version segment and marks every asset no-cache.
func revalidateStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if prefix := versionedStatic.FindString(r.URL.Path); prefix != "" {
			clone := r.Clone(r.Context())
			clone.URL.Path = "/static/" + strings.TrimPrefix(r.URL.Path, prefix)
			clone.URL.RawPath = ""
			r = clone
		}
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}
