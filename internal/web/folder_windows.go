//go:build windows

package web

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

func nativeFolderPicker(initial string) (string, error) {
	script := `Add-Type -AssemblyName System.Windows.Forms; $d=New-Object System.Windows.Forms.FolderBrowserDialog; $d.Description='Choose a folder for the new Agent_b chat'; $d.SelectedPath=$args[0]; if($d.ShowDialog() -eq 'OK'){[Console]::Out.Write($d.SelectedPath)}else{exit 2}`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-STA", "-Command", script, initial)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("folder selection canceled")
	}
	selected := strings.TrimSpace(string(out))
	if selected == "" {
		return "", fmt.Errorf("folder selection canceled")
	}
	return selected, nil
}

// signInStartEnabled reads what Windows' Startup page shows: on when the Run value
// exists and its StartupApproved state is absent or has bit 0 clear (02 on, 03 off).
func signInStartEnabled() (bool, error) {
	run, err := registry.OpenKey(registry.CURRENT_USER, signInRunKey, registry.QUERY_VALUE)
	if err == registry.ErrNotExist {
		return false, nil
	} else if err != nil {
		return false, err
	}
	defer run.Close()
	if _, _, err := run.GetStringValue(signInValue); err == registry.ErrNotExist {
		return false, nil
	} else if err != nil {
		return false, err
	}
	approved, err := registry.OpenKey(registry.CURRENT_USER, signInApprovedKey, registry.QUERY_VALUE)
	if err != nil {
		return true, nil
	}
	defer approved.Close()
	state, _, err := approved.GetBinaryValue(signInValue)
	return err != nil || len(state) == 0 || state[0]&1 == 0, nil
}

func setSignInStart(enabled bool, command string) error {
	if !enabled {
		for _, path := range []string{signInRunKey, signInApprovedKey} {
			if key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.SET_VALUE); err == nil {
				err = key.DeleteValue(signInValue)
				key.Close()
				if err != nil && err != registry.ErrNotExist {
					return err
				}
			}
		}
		return nil
	}
	run, _, err := registry.CreateKey(registry.CURRENT_USER, signInRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer run.Close()
	if err := run.SetStringValue(signInValue, command); err != nil {
		return err
	}
	// Turning it on here also turns it on in Windows' page, whatever that page said.
	approved, _, err := registry.CreateKey(registry.CURRENT_USER, signInApprovedKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer approved.Close()
	return approved.SetBinaryValue(signInValue, []byte{2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
}
