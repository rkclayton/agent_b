//go:build windows

package nativepolicy

import (
	"fmt"
	"os"
	"os/user"
	"strings"
	"testing"
)

func TestInspectAccountReadsNativeAccountAndGroupState(t *testing.T) {
	missing, err := InspectAccount(fmt.Sprintf("agentb-missing-%d", os.Getpid()))
	if err != nil || missing.Exists {
		t.Fatalf("missing account=%+v err=%v", missing, err)
	}
	status, err := InspectAccount("agentb-svc")
	if err != nil {
		t.Skipf("operator service account is unavailable: %v", err)
	}
	if !status.Exists || !status.Enabled || status.Administrator || !status.UsersMember {
		t.Fatalf("service account policy=%+v", status)
	}
}

func TestEnsureAccountRefusesOperatorIdentityBeforeMutation(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	name := current.Username
	if at := strings.LastIndexAny(name, `\@`); at >= 0 {
		name = name[at+1:]
	}
	if err := EnsureAccount(name, []byte("must-not-be-used"), true); err == nil {
		t.Fatal("operator identity was accepted as the service account")
	}
}

func TestManagedACLRuleRoundTripOnDisposableDirectory(t *testing.T) {
	account, err := InspectAccount("agentb-svc")
	if err != nil || !account.Exists {
		t.Skip("operator service account is unavailable")
	}
	path := t.TempDir()
	if err := SetManagedACLRule(path, account.SID, 0x301bf, 3, false); err != nil {
		t.Fatal(err)
	}
	if ok, err := HasManagedACLRule(path, account.SID, 0x301bf, 3, false); err != nil || !ok {
		t.Fatalf("rule present=%t err=%v", ok, err)
	}
	if err := RemoveManagedACLRules(path, account.SID); err != nil {
		t.Fatal(err)
	}
	if ok, err := HasManagedACLRule(path, account.SID, 0x301bf, 3, false); err != nil || ok {
		t.Fatalf("rule remains=%t err=%v", ok, err)
	}
}

func TestFirewallAddressesCompareAsTypedRanges(t *testing.T) {
	if !addressSetsEqual("127.0.0.0/8,192.168.1.0/24", "127.0.0.0-127.255.255.255,192.168.1.0-192.168.1.255") {
		t.Fatal("equivalent address representations drifted")
	}
	if addressSetsEqual("10.0.0.0/8", "10.0.0.0/9") {
		t.Fatal("different address ranges compared equal")
	}
}

func TestFirewallCOMReportsAnAbsentDisposableRule(t *testing.T) {
	rule, found, err := readFirewallRule(fmt.Sprintf("AgentB-disposable-missing-%d", os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("unexpected rule: %+v", rule)
	}
}

func TestElevatedHelperRefusesUnsignedExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyAuthenticode(executable); err == nil {
		t.Fatal("unsigned Go test executable was trusted for elevation")
	}
}

func TestFirewallCOMReadsTypedExistingRule(t *testing.T) {
	rule, found, err := readFirewallRule(firewallBlockName)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Skip("operator firewall rule is absent")
	}
	if rule.Direction != 2 || rule.Action != 0 || !rule.Enabled || rule.Profiles == 0 || rule.LocalUsers == "" || rule.RemoteAddresses == "" {
		t.Fatalf("typed rule was incomplete: %+v", rule)
	}
}
