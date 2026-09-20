//go:build !windows

package main

// Elsewhere there is no message box and the log is the whole of the report.
func showInstallFailure(string, string) {}
