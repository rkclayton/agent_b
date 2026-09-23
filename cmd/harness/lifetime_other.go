//go:build !windows

package main

func processCreated(int) int64 { return 0 }

func processRunning(pid int, _ int64) bool { return false }

func watchSessionEnd(string, func(string), func()) {}

func activateExistingInstance(string, string) (int, bool) { return 0, false }
