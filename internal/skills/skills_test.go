package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, root, folder, text string) {
	t.Helper()
	dir := filepath.Join(root, folder)
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(text), 0o600)
}

func TestIndexValidationTrustAndImport(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "alpha", "---\nname: alpha\ndescription: Handles alpha reports.\n---\nRead only this body.\n")
	fixture(t, root, "beta", "---\nname: beta\ndescription: Handles beta reports.\n---\n")
	fixture(t, root, "bad", "---\nname: BAD\ndescription: invalid name\n---\n")
	settings := map[string]Setting{"alpha": {Enabled: true}, "beta": {Enabled: true}}
	list, block := Scan(root, settings)
	if len(list) != 3 || !strings.Contains(block, "alpha") || !strings.Contains(block, "beta") || strings.Contains(block, "BAD") {
		t.Fatalf("list=%+v\nblock=%s", list, block)
	}
	if list[0].Valid || list[0].Reason == "" {
		t.Fatalf("invalid missing: %+v", list)
	}
	settings = map[string]Setting{}
	_, block = Scan(root, settings)
	if block != "" {
		t.Fatalf("disabled block=%q", block)
	}

	source := filepath.Join(t.TempDir(), "report-kit")
	fixture(t, filepath.Dir(source), "report-kit", "---\nname: report-kit\ndescription: Builds invented fixture reports.\n---\nUse scripts/build.ps1 only when asked.\n")
	_ = os.MkdirAll(filepath.Join(source, "scripts"), 0o700)
	_ = os.WriteFile(filepath.Join(source, "scripts", "build.ps1"), []byte("'fixture'"), 0o600)
	setting, err := Import(root, source, 8<<20)
	if err != nil || setting.Enabled || !strings.Contains(setting.Source, source) {
		t.Fatalf("setting=%+v err=%v", setting, err)
	}
}

func TestWarningsAndTwentySkillIndex(t *testing.T) {
	root := t.TempDir()
	settings := map[string]Setting{}
	for i := 0; i < 20; i++ {
		name := "skill-" + string(rune('a'+i))
		fixture(t, root, name, "---\nname: "+name+"\ndescription: Handles one invented evaluation.\n---\nBody.\n")
		settings[name] = Setting{Enabled: true}
	}
	_, block := Scan(root, settings)
	if estimateTokens(block) >= 800 {
		t.Fatalf("index cost=%d\n%s", estimateTokens(block), block)
	}
}
