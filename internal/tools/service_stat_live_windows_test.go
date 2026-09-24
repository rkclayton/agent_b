//go:build windows

package tools

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"harness/internal/config"
	"harness/internal/credential"
)

// Item 2fy discovery (v0.70.2 → v0.71.0/W1): under the service account, can a
// stat that ACLs deny be told from a path that does not exist? Opt-in like the
// capability suite: AGENTB_CAPABILITY_LIVE=1 and AGENTB_CAPABILITY_DATA holding a
// copy of the operator's DPAPI shell credential. It reads no file content; it
// only stats, as the service account, and logs what Windows answers.
func TestServiceAccountStatTellsDeniedFromMissing(t *testing.T) {
	if os.Getenv("AGENTB_CAPABILITY_LIVE") != "1" {
		t.Skip("set AGENTB_CAPABILITY_LIVE=1 with approved disposable roots")
	}
	dataRoot := os.Getenv("AGENTB_CAPABILITY_DATA")
	if dataRoot == "" {
		t.Fatal("AGENTB_CAPABILITY_DATA is required")
	}
	cfg := config.Defaults(t.TempDir())
	store := credential.New(dataRoot)
	password, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	defer clearBytes(password)
	userHome := os.Getenv("USERPROFILE")
	cases := []struct {
		label, path string
	}{
		{"missing, public root", `C:\definitely\not\here-2fy.txt`},
		{"missing, invented eval folder", `C:\Users\Public\Documents\GitHub\eval-20260310-110411-1120\logic\logic.go`},
		{"missing, under the operator's home", filepath.Join(userHome, "definitely-missing-2fy.txt")},
		{"missing, deep under the operator's home", filepath.Join(userHome, "AppData", "Local", "definitely-missing-2fy", "x.txt")},
		{"exists, readable to all", `C:\Windows\win.ini`},
		{"exists, the operator's own file", filepath.Join(userHome, "NTUSER.DAT")},
	}
	for _, c := range cases {
		var statErr error
		_, runErr := runAsServiceFileIdentity(cfg.Shell.ServiceAccount, password, func() (string, error) {
			_, statErr = os.Lstat(c.path)
			return "", nil
		})
		if runErr != nil {
			t.Fatalf("impersonation: %v", runErr)
		}
		class := "exists"
		switch {
		case errors.Is(statErr, fs.ErrNotExist):
			class = "not-found"
		case errors.Is(statErr, fs.ErrPermission):
			class = "denied"
		case statErr != nil:
			class = "other: " + statErr.Error()
		}
		t.Logf("2fy stat as service account: %-45s %s", c.label, class)
	}
}
