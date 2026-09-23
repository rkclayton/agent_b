//go:build !windows

package main

import "fmt"

// Item 2hc: the host window is Windows-only, because WebView2 is. Everywhere
// else the browser path is the only path, and says so.
func runHostWindow(url, userDataDir, title, applicationRoot string) error {
	return fmt.Errorf("the host window needs WebView2, which exists only on Windows")
}

func requestHostWindowAction(string) bool { return false }

func hostWindowAvailable() (string, error) {
	return "", fmt.Errorf("the host window needs WebView2, which exists only on Windows")
}
