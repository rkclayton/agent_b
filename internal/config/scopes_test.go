package config

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Item 2ly (a): every setting is classified, once. A key in neither list is a
// failure, because absence is indistinguishable from an oversight — the rule
// item 2jg's telemetry allow-list already lives by, applied to a second list
// that has the same failure mode.
func TestEveryConfigurationKeyIsClassified2ly(t *testing.T) {
	encoded, err := json.Marshal(Defaults(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	whole := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &whole); err != nil {
		t.Fatal(err)
	}
	if len(whole) < 15 {
		t.Fatalf("only %d top-level keys read from the configuration; the scan has stopped finding them", len(whole))
	}
	for key := range whole {
		if _, known := ScopeOf(key); !known {
			t.Errorf("configuration key %q is not classified — say whether it belongs to the machine or to a profile", key)
		}
	}
	// And the reverse: a classification for a key the configuration no longer
	// has is a line that will outlive its subject.
	for _, key := range ClassifiedKeys() {
		if _, present := whole[key]; !present {
			t.Errorf("%q is classified and the configuration no longer has it", key)
		}
	}
	t.Logf("%d keys classified: %d per-profile (%s)", len(ClassifiedKeys()), len(ProfileScopedKeys()), strings.Join(ProfileScopedKeys(), ", "))
}

func TestAnUnclassifiedKeyIsNotSilentlyMachine2ly(t *testing.T) {
	if _, known := ScopeOf("a_key_nobody_classified"); known {
		t.Fatal("an invented key was classified, so the completeness test cannot fail")
	}
}

// Item 2lj (d) and rel-1.15.0's card: typography follows the operator.
func TestTypographyIsPerProfile2ly(t *testing.T) {
	scope, known := ScopeOf("chat")
	if !known || scope != ScopeProfile {
		t.Fatalf("chat (text size and typeface) is %v — item 2lj (d) asks for per-profile", scope)
	}
	// And the security posture is not, because a second operator on this machine
	// does not get to turn off the boundary for everyone.
	for _, key := range []string{"shell", "sandbox", "signing", "approval", "listen", "updates", "telemetry"} {
		if scope, _ := ScopeOf(key); scope != ScopeMachine {
			t.Errorf("%q is per-profile; it is a property of the machine", key)
		}
	}
}

// (b): the machine's value is a DEFAULT A PROFILE INHERITS, not a value it
// copies. The overlay stores only disagreement, so changing a default still
// reaches a profile that never expressed an opinion.
func TestAProfileInheritsRatherThanCopies2ly(t *testing.T) {
	machine := Defaults(t.TempDir())
	live := machine

	// A profile that agrees with everything stores nothing.
	overlay, err := ExtractOverlay(live, machine)
	if err != nil {
		t.Fatal(err)
	}
	if len(overlay) != 0 {
		t.Fatalf("a profile with no opinion stored %d section(s): %v", len(overlay), keysOf(overlay))
	}

	// It expresses one opinion. Only that section is stored.
	live.Chat.TextSize = "large"
	overlay, err = ExtractOverlay(live, machine)
	if err != nil {
		t.Fatal(err)
	}
	if len(overlay) != 1 {
		t.Fatalf("one opinion stored %d section(s): %v", len(overlay), keysOf(overlay))
	}
	if _, present := overlay["chat"]; !present {
		t.Fatalf("the opinion was not stored under chat: %v", keysOf(overlay))
	}

	// NOW THE MACHINE DEFAULT MOVES. The profile's own opinion survives; the
	// section it never touched follows the machine. That is the inheritance,
	// and a copy would have frozen both.
	machine.Chat.Typeface = "atkinson"
	machine.Run.QueueDepth = 9
	next := machine
	if err := ApplyOverlay(&next, overlay); err != nil {
		t.Fatal(err)
	}
	if next.Chat.TextSize != "large" {
		t.Errorf("the profile lost its own setting: text size %q", next.Chat.TextSize)
	}
	if next.Run.QueueDepth != 9 {
		t.Errorf("a new machine default did not reach a profile that never overrode it: queue depth %d, want 9", next.Run.QueueDepth)
	}
}

// A machine-wide key that turns up in a profile's file is IGNORED, not obeyed.
// A profile must not be able to move the listener or switch off the service
// identity for everybody who uses the computer.
func TestAProfileCannotOverrideAMachineSetting2ly(t *testing.T) {
	machine := Defaults(t.TempDir())
	machine.Listen = "127.0.0.1:8790"
	next := machine
	hostile := Overlay{
		"listen": json.RawMessage(`"0.0.0.0:80"`),
		"shell":  json.RawMessage(`{"service_account":{"enabled":false}}`),
	}
	if err := ApplyOverlay(&next, hostile); err != nil {
		t.Fatal(err)
	}
	if next.Listen != "127.0.0.1:8790" {
		t.Errorf("a profile moved the listener to %q", next.Listen)
	}
	if next.Shell.ServiceAccount.Enabled != machine.Shell.ServiceAccount.Enabled {
		t.Error("a profile changed the service identity for the machine")
	}
}

// (d): AN EXISTING INSTALL LOSES NOTHING. A configuration with no profile
// overlay at all — every install before this item — comes back byte for byte.
func TestAnExistingInstallIsUnchanged2ly(t *testing.T) {
	existing := Defaults(t.TempDir())
	existing.Chat.TextSize = "small"
	existing.Chat.Typeface = "opendyslexic"
	existing.Run.QueueDepth = 4
	existing.Context.SoftPct = 0.6
	existing.Memory.MaxTokens = 900
	existing.Listen = "127.0.0.1:8790"

	before, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	// This is what startup does to an install that has never had a profile
	// overlay: apply nothing.
	after := existing
	if err := ApplyOverlay(&after, nil); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(got) {
		t.Fatalf("an existing configuration changed.\nbefore: %s\nafter:  %s", before, got)
	}
	// And its values become that profile's values the first time it saves,
	// because they differ from a fresh machine default — nothing is reset.
	overlay, err := ExtractOverlay(existing, Defaults(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"chat", "run", "context", "memory"} {
		if _, present := overlay[expected]; !present {
			t.Errorf("the existing install's %s values were not carried into its profile", expected)
		}
	}
	restored := Defaults(t.TempDir())
	if err := ApplyOverlay(&restored, overlay); err != nil {
		t.Fatal(err)
	}
	if restored.Chat.TextSize != "small" || restored.Chat.Typeface != "opendyslexic" ||
		restored.Run.QueueDepth != 4 || restored.Memory.MaxTokens != 900 {
		t.Fatalf("a value was lost on the way into the profile: %+v", restored.Chat)
	}
}

// The patch splitter routes a dotted key by its first segment, which is the
// form the browser actually sends.
func TestAPatchIsSplitByScope2ly(t *testing.T) {
	profile, machine := ProfileScopedPatch(map[string]any{
		"chat.typeface":                 "atkinson",
		"run.queue_depth":               6,
		"reflection.floor.model_calls":  3,
		"listen":                        "127.0.0.1:8790",
		"shell.service_account.enabled": true,
		"updates.auto_check":            false,
	})
	wantProfile := []string{"chat.typeface", "reflection.floor.model_calls", "run.queue_depth"}
	wantMachine := []string{"listen", "shell.service_account.enabled", "updates.auto_check"}
	if got := sortedKeys(profile); !reflect.DeepEqual(got, wantProfile) {
		t.Errorf("profile half: %v, want %v", got, wantProfile)
	}
	if got := sortedKeys(machine); !reflect.DeepEqual(got, wantMachine) {
		t.Errorf("machine half: %v, want %v", got, wantMachine)
	}
}

func keysOf(overlay Overlay) []string {
	out := make([]string, 0, len(overlay))
	for key := range overlay {
		out = append(out, key)
	}
	return sortStrings(out)
}

func sortedKeys(values map[string]any) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return sortStrings(out)
}

func sortStrings(values []string) []string {
	for index := 1; index < len(values); index++ {
		for back := index; back > 0 && values[back] < values[back-1]; back-- {
			values[back], values[back-1] = values[back-1], values[back]
		}
	}
	return values
}

// rel-1.20.0/W6: a section that marshals as `{}` must still overwrite what was
// there.
//
// This is the trap that made the per-profile marking look right and behave
// wrong. ApplyOverlay merges into a copy of the live configuration, and
// unmarshalling an empty JSON object over a populated struct changes nothing at
// all -- so laying the machine's DEFAULT over a profile's value was a no-op
// whenever the default was the zero value, which is the common case. A new
// profile then read the previous profile's answer and looked like it had copied
// it.
func TestAZeroSectionStillOverwrites2ly(t *testing.T) {
	machine := Defaults(t.TempDir())
	baseline, err := AllProfileSections(machine)
	if err != nil {
		t.Fatal(err)
	}
	live := machine
	live.Chat.TextSize = "large"
	live.Chat.Typeface = "Comic Sans"
	live.Run.MaxTurns = 999
	if err := SelectProfile(&live, baseline, nil); err != nil {
		t.Fatal(err)
	}
	if live.Chat.TextSize != machine.Chat.TextSize || live.Chat.Typeface != machine.Chat.Typeface {
		t.Errorf("the machine's typography did not come back: %+v", live.Chat)
	}
	if live.Run.MaxTurns != machine.Run.MaxTurns {
		t.Errorf("run.max_turns = %d, want the machine's %d", live.Run.MaxTurns, machine.Run.MaxTurns)
	}
}

// And a profile's own value still wins over the baseline it is laid on.
func TestTheOverlayWinsOverTheBaseline2ly(t *testing.T) {
	machine := Defaults(t.TempDir())
	baseline, err := AllProfileSections(machine)
	if err != nil {
		t.Fatal(err)
	}
	live := machine
	live.Chat.TextSize = "large"
	overlay, err := ExtractOverlaySections(live, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := overlay["chat"]; !present {
		t.Fatal("the disagreement was not captured")
	}
	if _, present := overlay["listen"]; present {
		t.Error("a machine-wide key was captured into a profile's overlay")
	}
	other := machine
	if err := SelectProfile(&other, baseline, overlay); err != nil {
		t.Fatal(err)
	}
	if other.Chat.TextSize != "large" {
		t.Errorf("the overlay did not win: %q", other.Chat.TextSize)
	}
}

// Item 2m0 (d): a drifted machine file is DETECTABLE from its own contents, and
// the tell is exact rather than heuristic.
//
// ExtractOverlay writes a key into an overlay only when it disagrees with the
// baseline. So an overlay entry that equals the machine file's section cannot
// have arisen honestly: either the machine file moved toward the profile, or the
// entry would have been dropped. Nothing else makes them converge.
func TestADriftedMachineFileIsNamed2m0(t *testing.T) {
	machine := Defaults(t.TempDir())
	baseline, err := AllProfileSections(machine)
	if err != nil {
		t.Fatal(err)
	}

	// A profile disagrees about typography. Nothing has drifted yet.
	live := machine
	live.Chat.TextSize = "large"
	overlay, err := ExtractOverlaySections(live, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if drifted, err := DriftedSections(machine, overlay); err != nil || len(drifted) != 0 {
		t.Fatalf("a healthy machine file was called drifted: %v (%v)", drifted, err)
	}

	// Now an older build saves the MERGE over the machine file, which is exactly
	// what every writer did before this item. The machine's chat section becomes
	// the profile's.
	drifted := machine
	drifted.Chat.TextSize = "large"
	names, err := DriftedSections(drifted, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "chat" {
		t.Fatalf("the drifted section was not named: %v", names)
	}

	// And the consequence it warns about is real: a profile created now would
	// inherit "large" as though the machine had always meant it.
	fresh := drifted
	if err := SelectProfile(&fresh, mustSections(t, drifted), nil); err != nil {
		t.Fatal(err)
	}
	if fresh.Chat.TextSize != "large" {
		t.Errorf("the drift did not reach a new profile, so the warning would be wrong: %q", fresh.Chat.TextSize)
	}
}

func mustSections(t *testing.T, cfg Config) Overlay {
	t.Helper()
	out, err := AllProfileSections(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Item 2m0 (c): THE REJECTED FIX STAYS REJECTED, and this is the test that
// stops it being rediscovered.
//
// The obvious fix for drift is to teach Config.Save to keep whatever
// per-profile sections are already on disk. rel-1.20.0/W6 tried it and reverted
// it, because a caller whose write IS per-profile then has its write silently
// discarded -- the Connections route writes an agent's role, `agents` is
// per-profile, and preserving disk threw it away. The cost of rediscovering
// that is a release; the cost of this test is nine lines.
//
// So: Save writes what it is given, and the scope split happens in SaveMachine,
// which takes the baseline from MEMORY rather than reading the file back.
func TestSaveWritesWhatItIsGiven2m0(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "harness.json")
	cfg := Defaults(root)
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	// A per-profile section, written straight through Save.
	cfg.Agents = []Agent{{Name: "Coder", B: "local", Toolset: FullToolset()}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	back, _, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Agents) != 1 || back.Agents[0].Name != "Coder" {
		t.Fatalf("Save dropped a per-profile section it was asked to write: %+v", back.Agents)
	}

	// SaveMachine is where the split lives, and it keeps the BASELINE it was
	// handed rather than what the file happens to hold.
	baseline, err := AllProfileSections(Defaults(root))
	if err != nil {
		t.Fatal(err)
	}
	live := *back
	live.Chat.TextSize = "large"
	// The profile's OPINION: it disagrees about chat, so chat is the key the
	// machine file must keep its own version of.
	opinion, err := ExtractOverlaySections(live, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveMachine(live, baseline, opinion, path); err != nil {
		t.Fatal(err)
	}
	if after, _, _, err := Load(path); err != nil {
		t.Fatal(err)
	} else if after.Chat.TextSize == "large" {
		t.Error("SaveMachine carried the live profile's typography into the machine file")
	}
}

// rel-1.21.0/W4, found by the installer matrix rather than by a unit test: a
// machine write must not roll back a per-profile value the profile has NO
// OPINION about.
//
// The first version of SaveMachine restored every per-profile section from the
// baseline. On a first run the baseline is captured before setup has chosen
// anything, so saving the connection setup had just created restored the EMPTY
// agents list and the file failed its own validation with "at least one agent is
// required". Inheriting a machine value has to mean the machine value can still
// change; only a key the profile has overridden is the profile's to keep.
func TestAMachineWriteDoesNotRollBackAnUnopinionatedKey2m0(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "harness.json")
	machine := Defaults(root)
	machine.Agents = nil // a first run: nothing has been set up yet
	baseline, err := AllProfileSections(machine)
	if err != nil {
		t.Fatal(err)
	}

	// Setup writes the first agent. The profile has expressed no opinion about
	// anything, so this IS the machine's value and must land.
	live := machine
	live.Agents = []Agent{{Name: "Coder", B: "local", Toolset: FullToolset()}}
	// The profile's STORED overlay is empty: it has never expressed an opinion,
	// and a value setup created a moment ago is not one.
	if err := SaveMachine(live, baseline, Overlay{}, path); err != nil {
		t.Fatalf("the machine write was refused: %v", err)
	}
	back, _, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Agents) != 1 {
		t.Fatalf("setup's agent was rolled back to the baseline: %+v", back.Agents)
	}

	// And the protection still holds for a key the profile DOES own: the
	// opinion names it, so the machine file keeps its own.
	// And the protection still holds for a key the profile's own file DOES
	// carry: the stored overlay names chat, so the machine keeps its own.
	opinionated := *back
	opinionated.Chat.TextSize = "large"
	ownOpinion, err := ExtractOverlaySections(opinionated, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := ownOpinion["chat"]; !present {
		t.Fatal("the disagreement was not captured, so this proves nothing")
	}
	delete(ownOpinion, "agents") // the machine's, established above
	if err := SaveMachine(opinionated, baseline, ownOpinion, path); err != nil {
		t.Fatal(err)
	}
	if after, _, _, err := Load(path); err != nil {
		t.Fatal(err)
	} else if after.Chat.TextSize == "large" {
		t.Error("a profile's typography reached the machine file")
	} else if len(after.Agents) != 1 {
		t.Errorf("the machine's own agents were lost: %+v", after.Agents)
	}
}
