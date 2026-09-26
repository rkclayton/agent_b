package tools

import (
	"context"
	"strings"
	"testing"

	"harness/internal/config"
)

// Item 2l2. Production v1.13.0, 2026-09-25: `shell.service_account.enabled:
// false`, the run loop correctly reporting `tool identity: process (service split
// disabled)`, and then ten `ALARM: service-account test failed; operator approval
// required: service-account working directory is unavailable` lines between
// 17:03:22 and 17:07:06 — one per Settings render, for an identity the product was
// not using.
func TestWithTheSplitOffNothingTestsTheServiceAccount2l2(t *testing.T) {
	shell := NewShell(config.Shell{ServiceAccount: config.ShellServiceAccount{Enabled: false, Account: "agentb-svc", Domain: "."}})
	for attempt := 0; attempt < 3; attempt++ {
		message, err := shell.TestServiceAccount(context.Background())
		if err != nil {
			t.Fatalf("the disabled split reported a failure: %v", err)
		}
		if !strings.Contains(message, "disabled") {
			t.Fatalf("the disabled split said %q", message)
		}
	}
	if status := shell.IdentityStatus(); status.OperatorApprovalRequired {
		t.Fatal("approval was demanded for an identity that is not in use")
	}
}

// (c): with the split enabled and the account unusable, the same condition is
// reported once, not once per trigger.
func TestAnUnusableAccountAlarmsOncePerCondition2l2(t *testing.T) {
	shell := NewShell(config.Shell{ServiceAccount: config.ShellServiceAccount{Enabled: true, Account: "agentb-svc", Domain: "."}})
	first, err := shell.failedServiceTest(`the service account cannot use its working directory: open shell folder "C:\gone": no such directory`, context.Canceled)
	if err == nil {
		t.Fatal("a failed test did not report an error")
	}
	if !strings.Contains(first, "cannot use its working directory") || !strings.Contains(first, `C:\gone`) {
		t.Fatalf("the condition does not name the directory: %s", first)
	}
	if previous, _ := shell.alarmedCondition.Load().(string); previous != first {
		t.Fatalf("the condition was not recorded: %q", previous)
	}
	// A different condition is a different alarm; the same one is not repeated.
	_, _ = shell.failedServiceTest("service-account credential is not stored", context.Canceled)
	if previous, _ := shell.alarmedCondition.Load().(string); previous != "service-account credential is not stored" {
		t.Fatalf("a new condition did not replace the old one: %q", previous)
	}
}
