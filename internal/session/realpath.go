package session

import (
	"os"
	"path/filepath"
)

// RealPath is where a path lands once the file system resolves every junction,
// symbolic link and reparse point on its way: the deepest ancestor that exists
// is resolved and the part not yet created is joined back on. Item 2fq: the
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
