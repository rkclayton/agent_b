package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"harness/internal/session"
)

// literalAbsolutePath finds Windows absolute paths written literally in a
// command or script: a drive path or a UNC path, up to a quote, whitespace or a
// shell separator.
var literalAbsolutePath = regexp.MustCompile(`(?i)(?:\b[a-z]:[\\/]|\\\\[a-z0-9._$-]+[\\/])[^"'\x60\s;|<>()\[\]{},]*`)

// quotedAbsolutePath is an absolute path inside quotes, which may hold spaces.
var quotedAbsolutePath = regexp.MustCompile(`(?i)["']((?:[a-z]:[\\/]|\\\\[a-z0-9._$-]+[\\/])[^"'\r\n]*)["']`)

// literalPaths are the quoted absolute paths whole, then the unquoted ones.
func literalPaths(source string) []string {
	paths := []string{}
	for _, match := range quotedAbsolutePath.FindAllStringSubmatch(source, -1) {
		paths = append(paths, match[1])
	}
	rest := quotedAbsolutePath.ReplaceAllString(source, " ")
	return append(paths, literalAbsolutePath.FindAllString(rest, -1)...)
}

// outsideLiteralPaths lists the absolute paths a command or script names that
// lie outside the chat's own folder and the plan repositories it may work in
// (item 2fi). An executable's path is not a file the script reads, so a path
// to an .exe that exists as a file is left alone; .cmd and .bat are text a
// script can read and are not (v0.69.0/W12 cold review: "win.ini .exe" and
// "id_rsa#.exe" rode that exemption). Paths built at runtime are not seen:
// with no service identity this is the card's ergonomics, not a boundary.
func outsideLiteralPaths(source string, s *session.Session) []string {
	roots := []string{s.Workspace}
	if s.PlanRepos != nil {
		roots = append(roots, s.PlanRepos()...)
	}
	seen := map[string]bool{}
	outside := []string{}
	for _, match := range literalPaths(source) {
		// Clean first: trimming a sentence's full stop before cleaning turned
		// `C:\ws\chat\..` into the folder itself.
		candidate := filepath.Clean(match)
		if strings.HasSuffix(candidate, ".") && !strings.HasSuffix(candidate, "..") {
			candidate = filepath.Clean(strings.TrimSuffix(candidate, "."))
		}
		if strings.EqualFold(filepath.Ext(candidate), ".exe") {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				continue
			}
		}
		inside := false
		for _, root := range roots {
			if root != "" && pathInsideRoot(root, candidate) {
				inside = true
				break
			}
		}
		if !inside && !seen[strings.ToLower(candidate)] {
			seen[strings.ToLower(candidate)] = true
			outside = append(outside, candidate)
		}
	}
	return outside
}

func pathInsideRoot(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// outsideReadReason is the card's reason for a command or script that names
// paths outside the folder, or "" when it names none.
func outsideReadReason(source string, s *session.Session) string {
	paths := outsideLiteralPaths(source, s)
	if len(paths) == 0 {
		return ""
	}
	// Every path is named, up to a bound, so one cannot hide behind three.
	if len(paths) > 12 {
		paths = append(paths[:12], fmt.Sprintf("and %d more", len(paths)-12))
	}
	return "names a path outside the folder: " + strings.Join(paths, ", ")
}
