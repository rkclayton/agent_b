package tools

import (
	"context"
	"harness/internal/credential"
	"strings"
	"testing"
)

func TestEmptyLoginVaultCreatesNoBrokerOrRing2yl(t *testing.T) {
	if newLoginCall == nil {
		t.Skip("Windows SSH helper")
	}
	out := newLockedBuffer(context.Background())
	call, err := newLoginCall(context.Background(), credential.NewVault(t.TempDir()), out)
	if call != nil {
		defer call.Close()
	}
	if err != nil || call != nil || len(out.ring) != 0 || len(out.loginEnv) != 0 {
		t.Fatalf("empty vault allocated login state: call=%v err=%v ring=%d env=%d", call != nil, err, len(out.ring), len(out.loginEnv))
	}
}

func TestCleanConsoleOutputStripsANSIAndRepairsEncoding(t *testing.T) {
	raw := "\x1b[31;1mred\x1b[0m\n\x1b]0;title\x07ok" + string([]byte{0xff})
	clean := cleanConsoleOutput(raw)
	if clean != "red\nok�" || strings.Contains(clean, "\x1b") {
		t.Fatalf("clean=%q", clean)
	}
}
