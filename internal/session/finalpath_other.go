//go:build !windows

package session

import "path/filepath"

func finalPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
