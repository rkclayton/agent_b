//go:build !windows

package web

import "fmt"

func nativeFolderPicker(string) (string, error) {
	return "", fmt.Errorf("native folder picker is available only on Windows")
}

func signInStartEnabled() (bool, error) { return false, nil }

func setSignInStart(bool, string) error {
	return fmt.Errorf("starting at sign-in is available only on Windows")
}

func osVersion() string { return "other" }

func totalRAM() int64 { return 0 }
