//go:build !windows

package main

import "errors"

func startBrowserDetached(string, []string, string) (int, error) {
	return 0, errors.New("browser commands require Windows")
}
func killBrowserProcess(int) {}
