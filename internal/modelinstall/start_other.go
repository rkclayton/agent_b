//go:build !windows

package modelinstall

import "errors"

func startDetached(string, []string, string) (int, error) {
	return 0, errors.New("local model installation is supported only on Windows")
}
