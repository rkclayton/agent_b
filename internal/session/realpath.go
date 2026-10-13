package session

import (
	"os"
	"path/filepath"
	"strings"
)

// RealPath is where a path lands once the file system resolves every junction,
// symbolic link and reparse point on its way: the deepest ancestor that exists
// is resolved and the part not yet created is joined back on. the
// file tools compare this, not the path text, against the plans folder.
func RealPath(path string) (string, error) {
	clean, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	existing := clean
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return clean, nil
		}
		existing = parent
	}
	resolved, err := finalPath(existing)
	if err != nil {
		return "", err
	}
	rest, err := filepath.Rel(existing, clean)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, rest), nil
}

func SamePath(left, right string) bool {
	if leftReal, err := RealPath(left); err == nil {
		left = leftReal
	}
	if rightReal, err := RealPath(right); err == nil {
		right = rightReal
	}
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func PathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return true
	}
	root, err = RealPath(root)
	if err != nil {
		return false
	}
	candidate, err = RealPath(candidate)
	if err != nil {
		return false
	}
	rel, err = filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
