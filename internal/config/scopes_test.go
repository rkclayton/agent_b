package config

import (
	"encoding/json"
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
