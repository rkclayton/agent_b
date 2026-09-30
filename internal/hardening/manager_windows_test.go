//go:build windows

package hardening

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatusReportsAbsentAccountWithoutMutation(t *testing.T) {
	application := filepath.Join(t.TempDir(), "application")
	data := filepath.Join(t.TempDir(), "data")
	workspace := filepath.Join(t.TempDir(), "workspace")
	exchange := filepath.Join(t.TempDir(), "exchange")
	for _, path := range []string{application, data, workspace, exchange} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewNative()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	status, err := manager.Status(ctx, Request{AccountName: "agentb-test-account-that-does-not-exist", ApplicationDirectory: application, DataDirectory: data, WorkspaceDirectory: workspace, ExchangeDirectory: exchange})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || status.Applied || status.ACL.AccountExists || status.Firewall.AccountExists {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestVerifyDefersToStructuredStatusInspection(t *testing.T) {
	manager := &windowsManager{}
	result, err := manager.Run(context.Background(), "verify", Request{})
	if err != nil || !result.Attempted {
		t.Fatalf("Run(verify) = (%+v, %v)", result, err)
	}
}

// Item 2fe: a fresh install's workspace is <data>\scratch. That is part of the
// operator-data tree by design, so Security's status inspection succeeds; a
// workspace elsewhere inside the data root is still refused.
func TestStatusAcceptsTheScratchWorkspaceInsideTheDataRoot(t *testing.T) {
	application := filepath.Join(t.TempDir(), "application")
	data := filepath.Join(t.TempDir(), "data")
	exchange := filepath.Join(t.TempDir(), "exchange")
	profileScratch := filepath.Join(data, "profiles", "Operator", "scratch")
	for _, path := range []string{application, filepath.Join(data, "scratch"), profileScratch, filepath.Join(data, "profiles", "Operator", "other"), exchange} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewNative()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	request := Request{AccountName: "agentb-test-account-that-does-not-exist", ApplicationDirectory: application, DataDirectory: data, WorkspaceDirectory: filepath.Join(data, "scratch"), ExchangeDirectory: exchange}
	if status, err := manager.Status(ctx, request); err != nil || !status.Supported {
		t.Fatalf("a scratch workspace must inspect: %+v %v", status, err)
	}
	request.WorkspaceDirectory = profileScratch
	if status, err := manager.Status(ctx, request); err != nil || !status.Supported {
		t.Fatalf("a profile scratch workspace must inspect: %+v %v", status, err)
	}
	request.WorkspaceDirectory = filepath.Join(data, "profiles", "Operator", "other")
	if _, err := manager.Status(ctx, request); err == nil || !strings.Contains(err.Error(), "disjoint") {
		t.Fatalf("a non-scratch workspace inside the data root must still be refused: %v", err)
	}
}
