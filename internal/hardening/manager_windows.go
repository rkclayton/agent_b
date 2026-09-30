//go:build windows

package hardening

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"harness/internal/nativepolicy"
)

var procIsUserAnAdmin = syscall.NewLazyDLL("shell32.dll").NewProc("IsUserAnAdmin")

type windowsManager struct{}

// New keeps the cross-platform constructor stable. The former script paths are
// ignored because Windows host policy is now inspected and applied natively.
func New(string, string, string) Manager { return NewNative() }
func NewNative() Manager                 { return &windowsManager{} }

func (m *windowsManager) Status(ctx context.Context, request Request) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	account, err := nativepolicy.InspectAccount(request.AccountName)
	if err != nil {
		return Status{}, err
	}
	acl := ComponentStatus{Supported: true, AccountExists: account.Exists}
	if !account.Exists {
		if err := nativepolicy.ValidateACLPolicy(nativepolicy.ACLRequest{Application: request.ApplicationDirectory, Data: request.DataDirectory, Workspace: request.WorkspaceDirectory, Exchange: request.ExchangeDirectory}); err != nil {
			return Status{}, fmt.Errorf("inspect ACL policy: %w", err)
		}
		acl.Summary = "local service account is missing"
	} else {
		drift, inspectErr := nativepolicy.InspectACLPolicy(nativepolicy.ACLRequest{Application: request.ApplicationDirectory, Data: request.DataDirectory, Workspace: request.WorkspaceDirectory, Exchange: request.ExchangeDirectory, SID: account.SID})
		if inspectErr != nil {
			return Status{}, fmt.Errorf("inspect ACL policy: %w", inspectErr)
		}
		acl.Applied, acl.Drift = len(drift) == 0, len(drift)
		if acl.Applied {
			acl.Summary = "root, plans, scratch, workspace, and exchange-folder ACL policy verified"
		} else {
			acl.Summary = fmt.Sprintf("%d ACL drift item(s)", len(drift))
		}
		for _, item := range drift {
			acl.Items = append(acl.Items, DriftItem{Path: item.Subject, Expected: item.Expected, Found: item.Found})
		}
	}
	firewall := ComponentStatus{Supported: true, AccountExists: account.Exists}
	if !account.Exists {
		firewall.Summary = "local service account is missing"
	} else {
		drift, inspectErr := nativepolicy.InspectFirewallPolicy(nativepolicy.FirewallRequest{SID: account.SID, AllowLocalNetwork: request.AllowLocalNetwork, LocalSubnets: request.LocalSubnets, AllowedRanges: request.AllowedModelRanges})
		if inspectErr != nil {
			return Status{}, fmt.Errorf("inspect firewall policy: %w", inspectErr)
		}
		firewall.Applied, firewall.Drift = len(drift) == 0, len(drift)
		if firewall.Applied {
			firewall.Summary = "user-scoped outbound policy verified"
		} else {
			firewall.Summary = "the outbound rule is missing or differs from this policy; apply protection again"
		}
		for _, item := range drift {
			firewall.Items = append(firewall.Items, DriftItem{Rule: item.Subject, Expected: item.Expected, Found: item.Found})
		}
	}
	return Status{Supported: true, HarnessElevated: isUserAnAdmin(), ACL: acl, Firewall: firewall, Applied: acl.Applied && firewall.Applied, AllowLocalNetwork: request.AllowLocalNetwork, ConfirmedLocalSubnets: append([]string(nil), request.LocalSubnets...), AllowedModelRanges: append([]string(nil), request.AllowedModelRanges...)}, nil
}

func isUserAnAdmin() bool { result, _, _ := procIsUserAnAdmin.Call(); return result != 0 }

func (m *windowsManager) Run(ctx context.Context, action string, request Request) (RunResult, error) {
	if action != "apply" && action != "verify" && action != "remove" {
		return RunResult{}, fmt.Errorf("hardening action must be apply, verify, or remove")
	}
	if action == "verify" {
		return RunResult{Attempted: true}, nil
	}
	directory, err := os.MkdirTemp("", "agentb-hardening-*")
	if err != nil {
		return RunResult{}, err
	}
	defer os.RemoveAll(directory)
	requestPath, resultPath := filepath.Join(directory, "request.json"), filepath.Join(directory, "result.json")
	native := nativepolicy.HelperRequest{Operation: "hardening", Action: action, Account: request.AccountName, ACL: nativepolicy.ACLRequest{Application: request.ApplicationDirectory, Data: request.DataDirectory, Workspace: request.WorkspaceDirectory, Exchange: request.ExchangeDirectory}, Firewall: nativepolicy.FirewallRequest{AllowLocalNetwork: request.AllowLocalNetwork, LocalSubnets: request.LocalSubnets, AllowedRanges: request.AllowedModelRanges}}
	if err := nativepolicy.WriteHelperRequest(requestPath, &native); err != nil {
		return RunResult{}, err
	}
	if err := nativepolicy.LaunchElevatedHelper(ctx, requestPath, resultPath); err != nil {
		return RunResult{}, err
	}
	encoded, err := os.ReadFile(resultPath)
	if err != nil {
		return RunResult{Attempted: true}, err
	}
	var result nativepolicy.HelperResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		return RunResult{Attempted: true}, err
	}
	if !result.OK {
		return RunResult{Attempted: true}, fmt.Errorf("elevated hardening failed: %s", result.Message)
	}
	return RunResult{Attempted: true}, nil
}

func (m *windowsManager) GrantPlan(ctx context.Context, account, repository string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	status, err := nativepolicy.InspectAccount(account)
	if err != nil {
		return err
	}
	if !status.Exists {
		return fmt.Errorf("service account is missing")
	}
	return nativepolicy.SetManagedACLRule(repository, status.SID, 0x301bf, 3, false)
}
