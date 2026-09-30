//go:build !windows

package main

import "fmt"

func runNativePerUserInstall(string, []string, string, *installLog) error {
	return fmt.Errorf("Agent_b installation is supported only on Windows")
}

func runNativeUninstall(string, string, bool, bool, int) error {
	return fmt.Errorf("Agent_b uninstall is supported only on Windows")
}
