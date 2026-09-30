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
