package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	productdocs "harness"
	"harness/internal/cron"
	"harness/internal/quietproc"
)

func TestPriceWatchComparesConfirmedSellerOffers(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell is unavailable")
	}
	root := t.TempDir()
	if err := EnsureIncluded(root); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, ".included", "price-watch", "compare.ps1")
	pages := map[string][]map[string]any{}
	pagesReady := make(chan struct{})
	sellers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-pagesReady
		encoded, err := json.Marshal(pages[r.URL.Path])
		if err != nil {
			http.Error(w, "fixture encoding failed", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `<html><script type="application/json">%s</script></html>`, encoded)
	}))
	defer sellers.Close()
	first, second, third := sellers.URL+"/first", sellers.URL+"/second", sellers.URL+"/third"
	watch := map[string]any{"name": "Fixture console", "identity": "fixture-1tb", "currency": "USD", "target": 115, "mode": "landed", "sellers": []string{first, second, third}}
	offer := func(source, identity string, price, shipping int, bundle bool) map[string]any {
		return map[string]any{"source": source, "identity": identity, "currency": "USD", "price": price, "shipping": shipping, "bundle": bundle, "available": true}
	}
	pages["/first"] = []map[string]any{offer(first, "fixture-1tb", 95, 20, false)}
	pages["/second"] = []map[string]any{offer(second, "fixture-1tb", 104, 10, false)}
	pages["/third"] = []map[string]any{offer(third, "fixture-1tb", 30, 0, true), offer(third, "fixture-2tb", 20, 0, false)}
	close(pagesReady)
	offers := []map[string]any{offer("https://other.example/offer", "fixture-1tb", 1, 0, false)}
	for _, source := range []string{first, second, third} {
		response, err := sellers.Client().Get(source)
		if err != nil {
			t.Fatal(err)
		}
		page, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		_, structured, _ := strings.Cut(string(page), `<script type="application/json">`)
		structured, _, _ = strings.Cut(structured, "</script>")
		var candidates []map[string]any
		if err != nil || json.Unmarshal([]byte(structured), &candidates) != nil {
			t.Fatalf("read synthetic seller page: %v", err)
		}
		offers = append(offers, candidates...)
	}
	type reply struct {
		Best   struct{ Landed float64 }
		Notice string
		Job    cron.Args
	}
	call := func(mode string) reply {
		t.Helper()
		input := filepath.Join(root, "input.json")
		body, err := json.Marshal(map[string]any{"slug": "fixture-console", "action": mode, "watch": watch, "offers": offers})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(input, body, 0o600); err != nil {
			t.Fatal(err)
		}
		output, err := quietproc.Quiet(exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-File", script, input)).CombinedOutput()
		if err != nil {
			t.Fatalf("price watch fixture: %v\n%s", err, output)
		}
		var got reply
		if err := json.Unmarshal(output, &got); err != nil {
			t.Fatalf("decode reply: %v\n%s", err, output)
		}
		return got
	}
	jobArgs := call("start").Job
	saved, err := cron.New(root, nil, nil).Apply(context.Background(), jobArgs)
	if err != nil || saved.Job.Prompt != "Check price watch fixture-console." {
		t.Fatalf("saved schedule prompt=%q, err=%v", saved.Job.Prompt, err)
	}
	for _, scenario := range []struct {
		name       string
		target     int
		last, tax  any
		wantPrice  float64
		wantNotice string
	}{
		{"first meets target", 114, nil, nil, 114, "tax not included"},
		{"first exceeds target", 110, nil, nil, 114, "[SILENT]"},
		{"new low", 110, 115, nil, 114, "tax not included"},
		{"duplicate alert", 120, 114, nil, 114, "[SILENT]"},
		{"tax applied", 126, nil, 10, 125.40, "tax included"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			watch["target"], watch["alerted"], watch["tax_pct"] = scenario.target, scenario.last, scenario.tax
			got := call("check")
			if got.Best.Landed != scenario.wantPrice || !strings.Contains(got.Notice, scenario.wantNotice) {
				t.Fatalf("landed=%v notice=%q", got.Best.Landed, got.Notice)
			}
		})
	}
	watch["mode"], watch["appeared"] = "appearance", false
	if got := call("check"); !strings.Contains(got.Notice, "now listed") {
		t.Fatalf("appearance notice=%q", got.Notice)
	}
	watch["appeared"] = true
	if got := call("check"); got.Notice != "[SILENT]" {
		t.Fatalf("repeat appearance=%q", got.Notice)
	}
}

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

func TestAgentHelpRebuildReadsTheShippedDocs(t *testing.T) {
	docs := fstest.MapFS{}
	for _, name := range []string{"docs/CONNECTORS.md", "docs/SKILLS.md", "docs/INSTALL.md"} {
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		docs[name] = &fstest.MapFile{Data: data}
	}
	original, err := productdocs.AgentHelp(docs)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := EnsureIncluded(root); err != nil {
		t.Fatal(err)
	}
	built, _, _, err := Load(root, "", "agent_b")
	if err != nil || !strings.HasSuffix(built, original) {
		t.Fatalf("installed skill must end with the generated help: %v", err)
	}
	for _, title := range []string{"Credentials", "Connectors", "Skills", "Scheduled jobs", "Notifications", "Settings pages"} {
		if strings.Count(original, "## "+title+"\n") != 1 {
			t.Fatalf("missing or repeated section %s", title)
		}
	}
	source := docs["docs/CONNECTORS.md"]
	source.Data = []byte(strings.Replace(string(source.Data), "enter each approved origin on its own line", "use the example origin format", 1))
	revised, err := productdocs.AgentHelp(docs)
	if err != nil || revised == original || !strings.Contains(revised, "use the example origin format") {
		t.Fatalf("changed documentation did not change generated help: %v", err)
	}
	_, index := Scan(root, "", map[string]Setting{"agent_b": {Enabled: false}})
	if strings.Contains(index, "agent_b ") {
		t.Fatal("disabled help skill was indexed")
	}
}

func TestIncludedSkillsOverrideUpdateAndIndexBudget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	stateRoot := filepath.Join(t.TempDir(), "state")
	if err := EnsureIncluded(root); err != nil {
		t.Fatal(err)
	}
	list, block := Scan(root, stateRoot, nil)
	if len(list) != 11 {
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
	if stateful != 3 {
		t.Fatalf("stateful=%d", stateful)
	}
	stateFile := filepath.Join(stateRoot, "price-watch", "watch.json")
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cost := estimateTokens(block); cost > 400 {
		t.Fatalf("included index cost=%d\n%s", cost, block)
	} else {
		t.Logf("eleven included index lines cost %d tokens", cost)
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
