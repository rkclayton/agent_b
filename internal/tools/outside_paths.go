package tools

import (
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
// (item 2fi). An executable's path is not a file the script reads, so .exe,
// .cmd and .bat paths are left alone. Paths built at runtime are not seen:
// with no service identity this is the card's ergonomics, not a boundary.
func outsideLiteralPaths(source string, s *session.Session) []string {
	roots := []string{s.Workspace}
	if s.PlanRepos != nil {
		roots = append(roots, s.PlanRepos()...)
	}
	seen := map[string]bool{}
	outside := []string{}
	for _, match := range literalPaths(source) {
		candidate := filepath.Clean(strings.TrimRight(match, `.\/`))
		switch strings.ToLower(filepath.Ext(candidate)) {
		case ".exe", ".cmd", ".bat":
			continue
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
	if len(paths) > 3 {
		paths = append(paths[:3], "…")
	}
	return "names a path outside the folder: " + strings.Join(paths, ", ")
}
