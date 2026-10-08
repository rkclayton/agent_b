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
	list, block := Scan(root, filepath.Join(root, "state"), settings)
	if len(list) != 3 || !strings.Contains(block, "alpha") || !strings.Contains(block, "beta") || strings.Contains(block, "BAD") {
		t.Fatalf("list=%+v\nblock=%s", list, block)
	}
	if list[0].Valid || list[0].Reason == "" {
		t.Fatalf("invalid missing: %+v", list)
	}
	settings = map[string]Setting{}
	list, block = Scan(root, filepath.Join(root, "state"), settings)
	if !list[1].Enabled || !list[2].Enabled || list[0].Enabled || !strings.Contains(block, "alpha") {
		t.Fatalf("discovered defaults list=%+v block=%q", list, block)
	}
	settings["alpha"] = Setting{Enabled: false}
	list, _ = Scan(root, filepath.Join(root, "state"), settings)
	if list[1].Enabled {
		t.Fatalf("explicitly disabled skill came back on: %+v", list[1])
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
	_, block := Scan(root, filepath.Join(root, "state"), settings)
	if estimateTokens(block) >= 800 {
		t.Fatalf("index cost=%d\n%s", estimateTokens(block), block)
	}
}

func TestIncludedSkillsOverrideUpdateAndIndexBudget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	stateRoot := filepath.Join(t.TempDir(), "state")
	if err := EnsureIncluded(root); err != nil {
		t.Fatal(err)
	}
	list, block := Scan(root, stateRoot, nil)
	if len(list) != 9 {
		t.Fatalf("included count=%d", len(list))
	}
	stateful := 0
	for _, item := range list {
		if !item.Included || item.Source != "included" || !item.Enabled {
			t.Fatalf("included default=%+v", item)
		}
		if item.State {
			stateful++
			if item.StatePath != filepath.Join(stateRoot, item.Name) {
				t.Fatalf("state path=%q", item.StatePath)
			}
		}
	}
	if stateful != 2 {
		t.Fatalf("stateful=%d", stateful)
	}
	stateFile := filepath.Join(stateRoot, "price-watch", "watch.json")
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cost := estimateTokens(block); cost > 360 {
		t.Fatalf("included index cost=%d\n%s", cost, block)
	}

	fixture(t, root, "action-items", "---\nname: action-items\ndescription: User override.\n---\nOverride body.\n")
	list, _ = Scan(root, stateRoot, nil)
	for _, item := range list {
		if item.Name == "action-items" && (item.Included || item.Description != "User override.") {
			t.Fatalf("override did not win: %+v", item)
		}
	}
	if err := os.RemoveAll(filepath.Join(root, "action-items")); err != nil {
		t.Fatal(err)
	}
	includedPath := filepath.Join(root, ".included", "action-items", "SKILL.md")
	if err := os.WriteFile(includedPath, []byte("old shipped bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIncluded(root); err != nil {
		t.Fatal(err)
	}
	if state, err := os.ReadFile(stateFile); err != nil || string(state) != "kept" {
		t.Fatalf("state after update=%q err=%v", state, err)
	}
	body, path, _, err := Load(root, stateRoot, "action-items")
	if err != nil || path != includedPath || !strings.Contains(body, "Turn notes") {
		t.Fatalf("included restore path=%q err=%v body=%q", path, err, body)
	}
	list, _ = Scan(root, stateRoot, map[string]Setting{"action-items": {Enabled: false}})
	for _, item := range list {
		if item.Name == "action-items" && item.Enabled {
			t.Fatalf("explicit off was lost: %+v", item)
		}
	}
}
