package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// v0.68.0/W16: a registration is judged on the folder the path resolves to, so
// neither a link, an ancestor, a root nor a share brings the plans folder into
// a repository that b chats may write.
func TestRegistrationRefusalJudgesTheResolvedFolder(t *testing.T) {
	data := t.TempDir()
	plans := filepath.Join(data, "plans")
	if err := os.MkdirAll(filepath.Join(plans, "p1"), 0o700); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if resolved, reason := RegistrationRefusal(plans, repo); reason != "" || !strings.EqualFold(resolved, repo) {
		t.Fatalf("an ordinary repository was refused: %q %q", resolved, reason)
	}
	for name, path := range map[string]string{
		"missing":       filepath.Join(t.TempDir(), "absent"),
		"inside plans":  filepath.Join(plans, "p1"),
		"the data root": data,
		"drive root":    filepath.VolumeName(repo) + `\`,
	} {
		if _, reason := RegistrationRefusal(plans, path); reason == "" {
			t.Errorf("%s (%s) was accepted", name, path)
		}
	}
	if runtime.GOOS == "windows" {
		if _, reason := RegistrationRefusal(plans, `\\nonexistent-host\share\repo`); !strings.Contains(reason, "network") {
			t.Errorf("a share must be refused before it is touched: %q", reason)
		}
		link := filepath.Join(t.TempDir(), "j")
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, plans).CombinedOutput(); err != nil {
			t.Fatalf("mklink: %v %s", err, out)
		}
		t.Cleanup(func() { os.Remove(link) })
		if _, reason := RegistrationRefusal(plans, link); !strings.Contains(reason, "inside the plans folder") {
			t.Errorf("a junction to the plans folder must be refused as the plans folder: %q", reason)
		}
		// The registry refuses it too, whatever route asked.
		registry := &Registry{plansRoot: plans}
		if _, _, err := registry.EnsurePlan(link); err == nil {
			t.Error("EnsurePlan registered a junction into the plans folder")
		}
	}
}
