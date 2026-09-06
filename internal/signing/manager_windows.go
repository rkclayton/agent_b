//go:build windows

package signing

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type windowsManager struct {
	script, powershell string
}

func New(script string) Manager {
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(powershell); err != nil {
		powershell = "powershell.exe"
	}
	return &windowsManager{script: script, powershell: powershell}
}

func (m *windowsManager) Status(ctx context.Context, request Request) (Status, error) {
	var result Status
	err := m.run(ctx, "Verify", request, &result)
	return result, err
}
func (m *windowsManager) Create(ctx context.Context, request Request) (Result, error) {
	var result Result
	err := m.run(ctx, "Create", request, &result)
	return result, err
}
func (m *windowsManager) Import(ctx context.Context, request Request) (Result, error) {
	var result Result
	err := m.run(ctx, "Import", request, &result)
	clear(request.Password)
	clear(request.PFX)
	return result, err
}
func (m *windowsManager) Select(ctx context.Context, request Request) (Result, error) {
	var result Result
	err := m.run(ctx, "Select", request, &result)
	return result, err
}
func (m *windowsManager) Export(ctx context.Context, request Request) ([]byte, error) {
	var result struct {
		CER string `json:"cer_base64"`
	}
	if err := m.run(ctx, "Export", request, &result); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(result.CER)
}
func (m *windowsManager) Sign(ctx context.Context, request Request) (Result, error) {
	var result Result
	err := m.run(ctx, "Sign", request, &result)
	return result, err
}

func (m *windowsManager) run(ctx context.Context, action string, request Request, target any) error {
	payload, err := json.Marshal(map[string]any{
		"thumbprint": request.Thumbprint, "timestamp_url": request.TimestampURL,
		"expected_hash": request.ExpectedHash, "process_id": request.ProcessID,
		"pfx_base64": base64.StdEncoding.EncodeToString(request.PFX), "password": string(request.Password),
	})
	if err != nil {
		return err
	}
	defer clear(payload)
	command := exec.CommandContext(ctx, m.powershell, "-NoLogo", "-NoProfile", "-File", m.script, "-Action", action)
	command.Stdin = bytes.NewReader(payload)
	output, runErr := command.CombinedOutput()
	if runErr != nil {
		return fmt.Errorf("%s: %s", strings.ToLower(action), safeOutput(output, runErr))
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1]) == "" {
		return fmt.Errorf("%s returned no result", action)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(lines[len(lines)-1])), target); err != nil {
		return fmt.Errorf("decode %s result: %w: %s", strings.ToLower(action), err, safeOutput(output, nil))
	}
	return nil
}

func safeOutput(output []byte, err error) string {
	value := strings.TrimSpace(string(output))
	if value == "" && err != nil {
		value = err.Error()
	}
	if len(value) > 2000 {
		value = value[len(value)-2000:]
	}
	return value
}

func clear(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
