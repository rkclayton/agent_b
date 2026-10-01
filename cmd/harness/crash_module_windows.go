//go:build windows

package main

import "syscall"

var getModuleHandleW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")

func executableModuleBase() uint64 {
	handle, _, _ := getModuleHandleW.Call(0)
	if handle == 0 {
		return 0
	}
	return uint64(handle)
}
