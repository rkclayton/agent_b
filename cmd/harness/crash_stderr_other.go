//go:build !windows

package main

// Item 2mt (a): the traceback redirect is a Windows launcher concern. Everywhere
// else stderr already goes somewhere the operator can read, so there is nothing to
// rescue and nothing to take away.
func keepRuntimeTraceback(string) {}
