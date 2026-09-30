//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func TestNativeUninstallRemovesExactDisposableRegistration(t *testing.T) {
	root := t.TempDir()
	application, data := filepath.Join(root, "application"), filepath.Join(root, "data")
	start, send := filepath.Join(root, "start"), filepath.Join(root, "send")
	for _, path := range []string{application, data, start, send} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(application, "Agent_b.exe"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	registration := fmt.Sprintf(`HKCU:\Software\Agent_b-Installer-Test-native-uninstall-%d`, time.Now().UnixNano())
	if err := writeUninstallRegistration(registration, map[string]any{"DisplayName": "Agent_b"}); err != nil {
		t.Fatal(err)
	}
	if err := runNativeUninstall(application, data, start, send, registration, true, true, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.OpenKey(registry.CURRENT_USER, registration[len(`HKCU:\`):], registry.QUERY_VALUE); err != registry.ErrNotExist {
		t.Fatalf("registration still opens: %v", err)
	}
	for _, path := range []string{application, data} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", path, err)
		}
	}
}
