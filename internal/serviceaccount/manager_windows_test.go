//go:build windows

package serviceaccount

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/credential"
)

func TestStatusUsesReadOnlyScriptInspection(t *testing.T) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatal(err)
	}
	account := "agentb-inspect-" + hex.EncodeToString(bytes)
	manager := New(filepath.Join("..", "..", "scripts", "setup-service-account.ps1"))
	// A cold hosted Windows runner can spend most of ten seconds starting
	// Windows PowerShell before this read-only inspection runs. Keep the test
	// bounded, but use the same cold-start allowance as the hardening status
	// inspection so concurrent CI jobs do not inherit a timing lottery.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, err := manager.Status(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || status.Account != account || status.Exists {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestPowerShellReadsGoDPAPICredentialWithoutExposingIt(t *testing.T) {
	root := t.TempDir()
	store := credential.New(root)
	passwordBytes := make([]byte, 24)
	if _, err := rand.Read(passwordBytes); err != nil {
		t.Fatal(err)
	}
	password := hex.EncodeToString(passwordBytes)
	if err := store.Write([]byte(password)); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "setup-service-account.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	powershell := New(script).(*windowsManager).powershell
	output, err := exec.Command(powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", script, "-ValidateCredentialStore", "-CredentialStore", store.Path()).CombinedOutput()
	if err != nil {
		t.Fatalf("credential bridge failed: %v: %s", err, output)
	}
	if !bytes.Contains(output, []byte("AGENTB_CREDENTIAL_STORE_VALID")) || bytes.Contains(output, []byte(password)) {
		t.Fatalf("unsafe or unexpected credential bridge output: %s", output)
	}
}

func TestElevatedCommandUsesUACAndHiddenHelper(t *testing.T) {
	command := elevatedCommand(`C:\Windows\powershell.exe`, []string{"-File", `C:\Program Files\AgentB\setup.ps1`, "-NoPrompt"})
	for _, wanted := range []string{"Start-Process", "-Verb RunAs", "-WindowStyle Hidden", "WaitForExit", `"C:\Program Files\AgentB\setup.ps1"`} {
		if !strings.Contains(command, wanted) {
			t.Fatalf("elevation command does not contain %q:\n%s", wanted, command)
		}
	}
}

func TestSetupStepLinesReportPassAndNamedFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service-identity.log")
	log := strings.Join([]string{
		"VALIDATED: the supplied credential successfully authenticated with LogonUser.",
		"AGENTB_HARDENING_STEP=protections PASS",
		"AGENTB_HARDENING_STEP=network FAILED exit 1: DRIFT AgentB-Svc-Outbound-Block: RemoteAddress",
		"AGENTB_ELEVATED_WRAPPER_EXIT 1",
	}, "\n")
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"account — PASS",
		"protections — PASS",
		"network — FAILED exit 1: DRIFT AgentB-Svc-Outbound-Block: RemoteAddress",
	}
	got := setupStepLines(path, true)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("step lines = %#v, want %#v", got, want)
	}
}
