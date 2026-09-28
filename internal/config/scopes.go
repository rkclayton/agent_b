package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// Item 2ly: a setting belongs to a profile, or to the machine, and which is
// stated.
//
// Before this, the configuration was one file for the machine and every setting
// in it was global, while a profile owned its own data directories — so two
// operators sharing a machine shared every preference and shared nothing else.
// Three sections were already per-profile (agents, deliver, notifications),
// written to the profile's own settings.json; this classifies the rest and
// gives the per-profile ones the same treatment.
//
// (a): the classification is the real deliverable, and it is HERE rather than in
// a report, so it is the thing the code reads and not a description of it.
//
// The rule that decided each one: MACHINE-WIDE is anything about the machine,
// the install, or the security posture — a second operator on the same computer
// gets the same answer whether they want it or not, because it is not about
// them. PER-PROFILE is anything about how a person reads and works.

// Scope says where a configuration section is stored.
type Scope int

const (
	// ScopeMachine is stored in harness.json and is the same for everyone.
	ScopeMachine Scope = iota
	// ScopeProfile is stored in the active profile's settings.json, with the
	// machine's value as the default a profile INHERITS rather than copies.
	ScopeProfile
)

// scopes is the classification, by the top-level configuration key. Every key
// the configuration holds appears exactly once; a key that appears in neither
// list is a test failure, because absence is indistinguishable from an oversight
// — the same rule item 2jg's telemetry allow-list lives by.
var scopes = map[string]Scope{
	// ------------------------------------------------------------ machine-wide
	"config_version": ScopeMachine, // the file's own format
	"listen":         ScopeMachine, // one port, one machine
	"workspace":      ScopeMachine, // where work happens on this computer
	"log_dir":        ScopeMachine,
	"profiles":       ScopeMachine, // the catalogue of profiles is not itself one
	"connections":    ScopeMachine, // endpoints and their probed capabilities
	"services":       ScopeMachine,
	"shell":          ScopeMachine, // the service identity and its boundary
	"sandbox":        ScopeMachine, // a security posture, not a preference
	"signing":        ScopeMachine,
	"updates":        ScopeMachine, // which build this computer runs
	"telemetry":      ScopeMachine, // the install id identifies an install
	"operator_files": ScopeMachine, // the mailbox lives in the data root
	"approval":       ScopeMachine, // what may run unattended is a safety decision
	"tools":          ScopeMachine, // limits and guards on what tools may do

	// ------------------------------------------------------------- per-profile
	"chat":       ScopeProfile, // item 2lj (d): typography and density
	"run":        ScopeProfile, // the dials a person tunes to their own patience
	"context":    ScopeProfile, // accounting and compaction thresholds
	"memory":     ScopeProfile, // a profile already owns its memory directory
	"reflection": ScopeProfile, // item 2ls's floor, which is a working preference
	// Item 2mx: the chat a spoken request lands in. A session belongs to a profile, so
	// switching profile must not send a voice request into another profile's chat.
	"voice":         ScopeProfile,
	"agents":        ScopeProfile, // already per-profile before this item
	"deliver":       ScopeProfile, // already per-profile
	"notifications": ScopeProfile, // already per-profile
}

// ScopeOf answers for one top-level key. The second result is false when the key
// has not been classified at all, which the suite refuses to ship.
func ScopeOf(key string) (Scope, bool) {
	scope, ok := scopes[key]
	return scope, ok
}

// ProfileScopedKeys is every key stored with the profile, sorted.
func ProfileScopedKeys() []string {
	out := []string{}
	for key, scope := range scopes {
		if scope == ScopeProfile {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// ClassifiedKeys is every key the classification knows, sorted, for the test
// that compares it against the configuration's own fields.
func ClassifiedKeys() []string {
	out := make([]string, 0, len(scopes))
	for key := range scopes {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Overlay is a profile's per-profile settings as RAW JSON, one entry per
// top-level key.
//
// (b): raw, and only the keys actually overridden, because that is what makes a
// machine value a DEFAULT A PROFILE INHERITS rather than a value it copies. A
// struct would have to store every field, and then changing the machine default
// would stop reaching profiles that never expressed an opinion — which is the
// behaviour this item exists to avoid.
type Overlay map[string]json.RawMessage

// ExtractOverlay reads the per-profile sections out of a full configuration,
// OMITTING every section that still agrees with the machine.
//
// The omission is (b), and it is the whole difference between inheriting a
// default and copying it. If the overlay stored every per-profile section, a
// profile would freeze the machine's values the first time anything was saved,
// and changing a default afterwards would silently stop reaching it. Storing
// only disagreement means a profile that has no opinion keeps getting the
// machine's answer, for as long as it has no opinion.
func ExtractOverlay(cfg Config, machine Config) (Overlay, error) {
	baseline, err := AllProfileSections(machine)
	if err != nil {
		return nil, err
	}
	return ExtractOverlaySections(cfg, baseline)
}

func sections(cfg Config) (map[string]json.RawMessage, error) {
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ApplyOverlay lays a profile's overrides over the machine configuration. A key
// the overlay does not carry keeps the machine's value, which is the
// inheritance (b) requires.
//
// It round-trips through JSON deliberately: the configuration's own
// UnmarshalJSON methods carry every default and migration, so an overlay that
// names a section gets that section's defaults for anything it leaves out.
func ApplyOverlay(cfg *Config, overlay Overlay) error {
	if len(overlay) == 0 {
		return nil
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	whole := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &whole); err != nil {
		return err
	}
	for key, value := range overlay {
		if scope, known := ScopeOf(key); !known || scope != ScopeProfile {
			// A machine-wide key in a profile's file is ignored rather than
			// obeyed: a profile must not be able to move the listener or turn
			// off the service identity for everyone.
			continue
		}
		whole[key] = value
	}
	merged, err := json.Marshal(whole)
	if err != nil {
		return err
	}
	next := *cfg
	// Empty every section the overlay replaces before the merge, so a section
	// that marshals as `{}` still overwrites what was there. See zeroSection.
	for key := range overlay {
		if scope, known := ScopeOf(key); known && scope == ScopeProfile {
			zeroSection(&next, key)
		}
	}
	if err := json.Unmarshal(merged, &next); err != nil {
		return err
	}
	ApplyDefaults(&next)
	*cfg = next
	return nil
}

// ProfileScopedPatch splits a Settings patch by scope, so the write path knows
// which file each half belongs in. A patch key that is not a top-level section
// — the dotted forms the browser sends, like "chat.typeface" — is routed by its
// first segment.
func ProfileScopedPatch(patch map[string]any) (profile map[string]any, machine map[string]any) {
	profile, machine = map[string]any{}, map[string]any{}
	for key, value := range patch {
		top := key
		if index := strings.IndexByte(key, '.'); index > 0 {
			top = key[:index]
		}
		if scope, known := ScopeOf(top); known && scope == ScopeProfile {
			profile[key] = value
			continue
		}
		machine[key] = value
	}
	return profile, machine
}

// AllProfileSections is every per-profile section of a configuration,
// unconditionally — unlike ExtractOverlay, which omits what still agrees with
// the machine.
//
// It exists for the RESET half of switching profiles. An overlay says what a
// profile disagrees about, and it is silent about everything else; laying it
// over whatever the previous profile left in memory would carry that profile's
// values into this one for every key this one has no opinion about. So a switch
// restores the machine's sections first and lays the overlay over those.
func AllProfileSections(machine Config) (Overlay, error) {
	values, err := sections(machine)
	if err != nil {
		return nil, err
	}
	out := Overlay{}
	for _, key := range ProfileScopedKeys() {
		if value, present := values[key]; present {
			out[key] = value
		}
	}
	return out, nil
}

// SelectProfile puts one profile's per-profile settings in force: the machine's
// values for every per-profile key, then this profile's overrides over them.
//
// rel-1.20.0/W6 found the reset missing. ApplyOverlay alone passed its own unit
// tests and still leaked, because nothing in a pure extract/apply pair can see
// that the configuration it is applying to already carries another profile's
// answers. A new profile read the previous profile's text size and looked like
// it had copied it.
func SelectProfile(cfg *Config, baseline Overlay, overlay Overlay) error {
	if err := ApplyOverlay(cfg, baseline); err != nil {
		return err
	}
	return ApplyOverlay(cfg, overlay)
}

// ExtractOverlaySections is ExtractOverlay against a BASELINE ALREADY TAKEN --
// the machine's sections as they were before any profile was laid over them,
// which is the only baseline that stays true once the live configuration is a
// merge.
func ExtractOverlaySections(cfg Config, baseline Overlay) (Overlay, error) {
	live, err := sections(cfg)
	if err != nil {
		return nil, err
	}
	out := Overlay{}
	for _, key := range ProfileScopedKeys() {
		value, present := live[key]
		if !present {
			continue
		}
		if same, ok := baseline[key]; ok && bytes.Equal(same, value) {
			continue
		}
		out[key] = value
	}
	return out, nil
}

// zeroSection empties the Config field a top-level JSON key names.
//
// It exists because of a trap in the overlay path. ApplyOverlay unmarshals the
// merged document over a COPY of the live configuration, and unmarshalling a
// section that happens to marshal as `{}` -- every field zero and omitempty --
// leaves the target's fields exactly as they were. So laying the machine's
// default over a profile's value did nothing at all whenever the machine's value
// was the zero one, which is the common case. Emptying the field first makes the
// merge mean what it reads as.
func zeroSection(cfg *Config, key string) {
	value := reflect.ValueOf(cfg).Elem()
	fields := value.Type()
	for index := 0; index < fields.NumField(); index++ {
		tag := fields.Field(index).Tag.Get("json")
		if name, _, _ := strings.Cut(tag, ","); name == key {
			field := value.Field(index)
			field.Set(reflect.Zero(field.Type()))
			return
		}
	}
}

// SaveMachine writes the machine's configuration file with the per-profile
// sections taken from BASELINE rather than from the configuration in memory.
//
// Item 2m0. The configuration a running harness holds is the MERGE -- the
// machine's values with the active profile's laid over them -- and every writer
// of the machine file marshals that whole thing. So a route that came to change
// a connection wrote the active profile's typography into the machine's
// defaults as a side effect, and the next profile created inherited them. The
// nineteen callers do not each need to know this; the boundary does.
//
// This is NOT Config.Save taught to preserve what is on disk. That was tried at
// rel-1.20.0/W6 and reverted, because reading the sections back from the file
// drops a write the caller meant to make -- the Connections route's agent-role
// write is per-profile, and preserving disk silently discarded it. The baseline
// here is the machine's own values as they were before any profile was applied,
// held in memory, and a caller whose write IS per-profile writes it through the
// profile instead.
// OPINION is what the active profile currently disagrees with the baseline
// about. Only those keys are restored from the baseline; for every other key
// the live value IS the machine's value, because SelectProfile put the
// baseline there and nothing overrode it.
//
// rel-1.21.0/W4 found this the hard way. Restoring EVERY per-profile section
// from the baseline rolled back writes the machine was entitled to make: on a
// first run the baseline is captured before setup has chosen anything, so
// saving a new connection restored the empty agents list and the file failed
// its own validation with "at least one agent is required". A profile that has
// expressed no opinion about a key must still be able to have the machine's
// value changed under it -- that is what inheriting means.
func SaveMachine(cfg Config, baseline Overlay, opinion Overlay, path string) error {
	restore := Overlay{}
	for key := range opinion {
		if value, ok := baseline[key]; ok {
			restore[key] = value
		}
	}
	if len(restore) == 0 {
		return cfg.Save(path)
	}
	machine := cfg
	if err := ApplyOverlay(&machine, restore); err != nil {
		return err
	}
	// THE WRITE IS NEVER LOST. If the machine's own values are not a viable
	// configuration by themselves -- which is exactly true during a first run,
	// where the baseline predates setup and carries no agent at all -- then the
	// merge is written instead and the caller's change lands.
	//
	// rel-1.21.0/W4 found this twice, both times as "agents: at least one agent
	// is required" from the onboarding gate. Refusing the write to protect the
	// machine's defaults is the same mistake as the fix this item rejected: it
	// puts tidiness above the operator's change. Separation is worth having
	// until it costs a write, and then the write wins.
	if err := machine.Validate(); err != nil {
		return cfg.Save(path)
	}
	return machine.Save(path)
}

// DriftedSections names the per-profile sections of a machine configuration that
// a profile's own values have leaked into. Item 2m0 (d).
//
// The tell is exact rather than heuristic. ExtractOverlay writes a key into a
// profile's overlay ONLY when it disagrees with the machine baseline, so an
// overlay entry that now EQUALS the machine file's section is proof the machine
// file moved toward that profile: the entry could not have been written
// otherwise, and nothing else makes them converge.
//
// It reports and does not repair, which is the narrowing rel-1.21.0/W0 took
// deliberately: the value the section drifted FROM is gone, and substituting a
// default would silently overwrite a machine setting the operator meant. A named
// finding lets the operator decide; a guess would not.
func DriftedSections(machine Config, overlay Overlay) ([]string, error) {
	if len(overlay) == 0 {
		return nil, nil
	}
	current, err := sections(machine)
	if err != nil {
		return nil, err
	}
	var drifted []string
	for _, key := range ProfileScopedKeys() {
		value, present := overlay[key]
		if !present {
			continue
		}
		if same, ok := current[key]; ok && bytes.Equal(same, value) {
			drifted = append(drifted, key)
		}
	}
	sort.Strings(drifted)
	return drifted, nil
}
