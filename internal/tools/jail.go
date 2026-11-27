package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"harness/internal/credential"
	"harness/internal/session"
)

var knownFolderPath = func(name string) (string, bool) {
	base, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(base, strings.Title(name)), true
}
var driveName = regexp.MustCompile(`(?i)^(?:my |the )?([a-z])(?::| drive(?: root)?)$`)

// ResolveNamedPath turns the operator's ordinary Windows folder names into paths.
func ResolveNamedPath(value string) string {
	trimmed := strings.TrimSpace(value)
	name := strings.ToLower(trimmed)
	name = strings.TrimPrefix(strings.TrimPrefix(name, "my "), "the ")
	for _, known := range []string{"downloads", "documents", "desktop", "pictures"} {
		if name == known {
			if path, ok := knownFolderPath(known); ok {
				return filepath.Clean(path)
			}
		}
	}
	if match := driveName.FindStringSubmatch(trimmed); len(match) == 2 {
		return strings.ToUpper(match[1]) + `:\`
	}
	return value
}

func Resolve(workspace, path string) (string, error) {
	return resolvePath(workspace, path, true)
}

type osPathPolicyKey struct{}

func withOSPathPolicy(ctx context.Context) context.Context {
	return context.WithValue(ctx, osPathPolicyKey{}, true)
}

func resolveForSessionTool(ctx context.Context, s *session.Session, root, path string) (string, error) {
	// Paths use the current filesystem identity.
	resolved, err := resolvePath(root, s.ResolveOperatorPath(path), false)
	if err != nil {
		return "", err
	}
	if protectedCredentialPath(resolved) {
		return "", fmt.Errorf("credential store rule: file tools cannot access stored credential files")
	}
	return resolved, nil
}

func protectedCredentialPath(path string) bool {
	if credential.IsStoragePath(path) {
		return true
	}
	real, err := session.RealPath(path)
	return err == nil && credential.IsStoragePath(real)
}

func within(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func resolvePath(workspace, path string, enforceWorkspace bool) (string, error) {
	path = ResolveNamedPath(path)
	if path == "" {
		path = "."
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	candidate := filepath.FromSlash(path)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate = filepath.Clean(candidate)
	// In OS-authorized modes Windows, rather than the workspace jail, is the
	// boundary. Avoid resolving every ancestor here: EvalSymlinks opens parent
	// directories for metadata access and can reject an otherwise traversable,
	// ACL-authorized target under a private user connection.
	if !enforceWorkspace {
		return candidate, nil
	}
	// cold review: both sides are judged where the file system
	// takes them. filepath.EvalSymlinks does not follow a junction (Go 1.23+),
	// so a junction inside a folder used to pass the jail by its text path.
	root, err = session.RealPath(root)
	if err != nil {
		return "", err
	}
	candidate, err = session.RealPath(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path is outside the folder")
	}
	return candidate, nil
}

func refuseRepoPolicyWrite(workspace, path string) error {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	candidate := filepath.FromSlash(path)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	refused := func(root, candidate string) bool {
		rel, relErr := filepath.Rel(root, filepath.Clean(candidate))
		if relErr != nil {
			return false
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		return len(parts) > 0 && strings.EqualFold(parts[0], ".agentb")
	}
	if refused(root, candidate) {
		return fmt.Errorf("repo-policy immutability rule: model file tools cannot write .agentb/")
	}
	// Reach is OS-governed, but repository policy remains immutable even when a
	// junction or symbolic link gives .agentb a harmless-looking text path.
	if realRoot, rootErr := session.RealPath(root); rootErr == nil {
		if realCandidate, candidateErr := session.RealPath(candidate); candidateErr == nil && refused(realRoot, realCandidate) {
			return fmt.Errorf("repo-policy immutability rule: model file tools cannot write .agentb/")
		}
	}
	return nil
}
