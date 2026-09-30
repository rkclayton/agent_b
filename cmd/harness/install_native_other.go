//go:build !windows

package main

import "fmt"

func runNativePerUserInstall(string, []string, string, *installLog) error {
	return fmt.Errorf("Agent_b installation is supported only on Windows")
}
