//go:build windows

package serviceaccount

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"harness/internal/nativepolicy"
)

type windowsManager struct{ workDir string }

// New keeps the cross-platform constructor stable. Windows no longer executes
// the former PowerShell implementation, so the script path is intentionally ignored.
func New(string) Manager { return NewNative() }

func NewNative() Manager { return &windowsManager{workDir: os.TempDir()} }

func (m *windowsManager) Status(ctx context.Context, account string) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	native, err := nativepolicy.InspectAccount(account)
	if err != nil {
		return Status{}, fmt.Errorf("inspect local service account: %w", err)
	}
	return Status{Supported: true, Account: account, Exists: native.Exists, Enabled: native.Enabled, Administrator: native.Administrator, UsersMember: native.UsersMember, LockedOut: native.LockedOut, HarnessElevated: isElevated()}, nil
}

func isElevated() bool {
	sid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	member, _ := windows.GetCurrentProcessToken().IsMember(sid)
	return member
}

func (m *windowsManager) Setup(ctx context.Context, account, credentialPath string, reset bool, protection *Protection) (SetupResult, error) {
	resultPath, logPath := m.resultPaths()
	requestPath := resultPath + ".request.json"
	defer os.Remove(requestPath)
	request := nativepolicy.HelperRequest{Operation: "provision", Action: "apply", Account: account, CredentialPath: credentialPath, Reset: reset}
	if protection != nil {
		request.ACL = nativepolicy.ACLRequest{Application: protection.ApplicationDirectory, Data: protection.DataDirectory, Workspace: protection.WorkspaceDirectory, Exchange: protection.ExchangeDirectory}
		request.Firewall = nativepolicy.FirewallRequest{AllowLocalNetwork: protection.AllowLocalNetwork, LocalSubnets: protection.LocalSubnets, AllowedRanges: protection.AllowedModelRanges}
	}
	if err := nativepolicy.WriteHelperRequest(requestPath, &request); err != nil {
		return SetupResult{}, err
	}
	if err := nativepolicy.LaunchElevatedHelper(ctx, requestPath, resultPath); err != nil {
		launch := LaunchStarted
		if strings.Contains(err.Error(), "declined") {
			launch = LaunchDeclined
		}
		return SetupResult{Attempted: launch == LaunchStarted, Launch: launch, LogPath: logPath}, err
	}
	encoded, err := os.ReadFile(resultPath)
	if err != nil {
		return SetupResult{Attempted: true, Launch: LaunchStarted, LogPath: logPath}, err
	}
	var native nativepolicy.HelperResult
	if err := json.Unmarshal(encoded, &native); err != nil {
		return SetupResult{Attempted: true, Launch: LaunchStarted, LogPath: logPath}, err
	}
	_ = os.WriteFile(logPath, []byte(strings.Join(append(native.Steps, native.Message), "\r\n")+"\r\n"), 0o600)
	result := &ElevatedResult{Ok: native.OK, Message: native.Message, Outcome: "ready", Account: account}
	setup := SetupResult{Attempted: true, Launch: LaunchStarted, Result: result, LogPath: logPath, Steps: native.Steps}
	if !native.OK {
		return setup, fmt.Errorf("%s (full output: %s)", native.Message, logPath)
	}
	return setup, nil
}

func (m *windowsManager) resultPaths() (string, string) {
	stamp := time.Now().UTC().Format("20060102-150405.000")
	dir := m.workDir
	if strings.TrimSpace(dir) == "" {
		dir = os.TempDir()
	}
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "service-identity-"+stamp+".result.json"), filepath.Join(dir, "service-identity-"+stamp+".log")
}
