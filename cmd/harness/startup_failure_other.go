//go:build !windows

package main

// showStartupFailure has no window to show off Windows; the log already holds the reason.
func showStartupFailure(string) {}
