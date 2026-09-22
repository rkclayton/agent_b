package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/updater"
)

func writeBundleFixture(t *testing.T, entries map[string]string) string {
	t.Helper()
	var payload bytes.Buffer
	archive := zip.NewWriter(&payload)
	for name, content := range entries {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Agent_b-setup.exe")
	content := append([]byte("MZ fixture"), payload.Bytes()...)
	content = append(content, installBundleMagic...)
	length := make([]byte, 8)
	binary.LittleEndian.PutUint64(length, uint64(payload.Len()))
	content = append(content, length...)
	hash := sha256.Sum256(payload.Bytes())
	content = append(content, hash[:]...)
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSingleFileSetupExtractsVerifiedPayloadAndMatchingManifest(t *testing.T) {
	executable := writeBundleFixture(t, map[string]string{"scripts/install-Agent_b.ps1": "$displayVersion = '1.5.0'", "web/index.html": "ok"})
	root, cleanup, found, err := extractInstallBundle(executable)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	defer cleanup()
	for _, relative := range []string{"scripts/install-Agent_b.ps1", "web/index.html", "Agent_b.exe", "candidate-final.json"} {
		if _, err := os.Stat(filepath.Join(root, relative)); err != nil {
			t.Errorf("%s: %v", relative, err)
		}
	}
	var manifest struct {
		ExeSHA string `json:"exe_sha256"`
	}
	data, _ := os.ReadFile(filepath.Join(root, "candidate-final.json"))
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	want, _ := fileSHA256(executable)
	if manifest.ExeSHA != want {
		t.Fatalf("manifest hash=%s want %s", manifest.ExeSHA, want)
	}
}

func TestSetupFilenameSelectsInstallMode(t *testing.T) {
	for _, path := range []string{
		`C:\Downloads\Agent_b-setup.exe`,
		`C:\Downloads\Agent_b-setup (1).exe`,
		`C:\Downloads\Agent_b-setup (27).EXE`,
	} {
		if !setupExecutable(path) {
			t.Errorf("%q must select install mode", path)
		}
	}
	for _, path := range []string{
		`C:\Program Files\Agent_b\Agent_b.exe`,
		`C:\Downloads\Agent_b-setup ().exe`,
		`C:\Downloads\Agent_b-setup (copy).exe`,
		`C:\Downloads\Agent_b-setup (1) copy.exe`,
		`C:\Downloads\Agent_b-setup-old.exe`,
	} {
		if setupExecutable(path) {
			t.Errorf("%q must not select install mode", path)
		}
	}
}

func TestUpdateFixtureURLAcceptsOnlyLoopback(t *testing.T) {
	t.Setenv("AGENTB_UPDATE_FIXTURE_URL", "http://127.0.0.1:4321/latest")
	if got := updateLatestURL(); got != "http://127.0.0.1:4321/latest" {
		t.Fatalf("loopback fixture URL=%q", got)
	}
	t.Setenv("AGENTB_UPDATE_FIXTURE_URL", "https://example.com/latest")
	if got := updateLatestURL(); got != updater.LatestReleaseURL {
		t.Fatalf("public override was accepted: %q", got)
	}
}

func TestSingleFileSetupRefusesTamperedPayload(t *testing.T) {
	executable := writeBundleFixture(t, map[string]string{"scripts/install-Agent_b.ps1": "ok"})
	file, err := os.OpenFile(executable, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0xff}, int64(len("MZ fixture")+4)); err != nil {
		t.Fatal(err)
	}
	file.Close()
	_, _, found, err := extractInstallBundle(executable)
	if !found || err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestSingleFileSetupFindsBundleBeforeAuthenticodeCertificate(t *testing.T) {
	executable := writeBundleFixture(t, map[string]string{"scripts/install-Agent_b.ps1": "ok"})
	file, err := os.OpenFile(executable, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("synthetic Authenticode certificate table")); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	root, cleanup, found, err := extractInstallBundle(executable)
	if err != nil || !found {
		t.Fatalf("extract signed-style bundle: found=%v err=%v", found, err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(root, "scripts", "install-Agent_b.ps1")); err != nil {
		t.Fatal(err)
	}
}

// Item 2gl (v1.2.0/W2). The marker is the answer to "should i re-run?", so
// these pin what makes its presence mean something.

func TestAnInstallMarkerSurvivesUntilAnInstallFinishes(t *testing.T) {
	root := t.TempDir()
	if _, found, err := readInstallMarker(root); err != nil || found {
		t.Fatalf("a fresh root has no marker: found=%v err=%v", found, err)
	}
	if err := writeInstallMarker(root, InstallMarker{Phase: "preflight", Version: "v1.2.0", Source: root}); err != nil {
		t.Fatal(err)
	}
	marker, found, err := readInstallMarker(root)
	if err != nil || !found {
		t.Fatalf("the marker was not read back: found=%v err=%v", found, err)
	}
	if marker.Phase != "preflight" || marker.Version != "v1.2.0" {
		t.Fatalf("marker lost its fields: %+v", marker)
	}
	if marker.StartedAt == "" || marker.UpdatedAt == "" || marker.PID == 0 {
		t.Fatalf("marker is missing when/who: %+v", marker)
	}
	// A later phase keeps the start time: the operator is told when the
	// install began, not when it last moved.
	started := marker.StartedAt
	marker.Phase = "copying the application"
	if err := writeInstallMarker(root, marker); err != nil {
		t.Fatal(err)
	}
	next, _, err := readInstallMarker(root)
	if err != nil {
		t.Fatal(err)
	}
	if next.StartedAt != started || next.Phase != "copying the application" {
		t.Fatalf("phase move rewrote the start: %+v", next)
	}
	// Only a finished install clears it.
	if err := clearInstallMarker(root); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := readInstallMarker(root); found {
		t.Fatal("the marker outlived the install that cleared it")
	}
	// Clearing again is not an error: an install that never wrote one still
	// finishes cleanly.
	if err := clearInstallMarker(root); err != nil {
		t.Fatalf("clearing an absent marker is not a failure: %v", err)
	}
}

func TestAnUnreadableMarkerIsReportedRatherThanIgnored(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, installMarkerName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err := readInstallMarker(root)
	if !found || err == nil {
		t.Fatalf("something wrote that file; pretending it is absent is how an interrupted install becomes invisible: found=%v err=%v", found, err)
	}
}

func TestTheInterruptedInstallLineAnswersTheOperatorsQuestion(t *testing.T) {
	line := describeInterruptedInstall(InstallMarker{Phase: "copying the application", Version: "v1.2.0", StartedAt: "2026-09-21T08:00:00Z"})
	for _, want := range []string{"did not finish", "copying the application", "v1.2.0", "safe to repeat"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the line must say %q: %s", want, line)
		}
	}
	// It must never be blank, whatever the marker holds.
	if bare := describeInterruptedInstall(InstallMarker{}); len(bare) < 40 {
		t.Fatalf("an empty marker still gets a sentence: %q", bare)
	}
}

// The installer's own parameters are passed through untouched, so the two
// vocabularies cannot drift.
func TestInstallArgumentsSplitByWhoOwnsThem(t *testing.T) {
	arguments := []string{
		"--install", "--quiet", "--install-data", `C:\data`,
		"-SourceDirectory", `C:\candidate`, "-TestMode",
		"-UninstallRegistryPath", `HKCU:\Software\X\Agent_b`,
	}
	mine := installFlagArgs(arguments)
	theirs := installPassthrough(arguments)
	for _, want := range []string{"--install", "--quiet", "--install-data", `C:\data`} {
		if !contains(mine, want) {
			t.Fatalf("this mode keeps %q: %v", want, mine)
		}
	}
	for _, want := range []string{"-SourceDirectory", `C:\candidate`, "-TestMode", "-UninstallRegistryPath", `HKCU:\Software\X\Agent_b`} {
		if !contains(theirs, want) {
			t.Fatalf("the installer keeps %q: %v", want, theirs)
		}
	}
	// Nothing this mode owns may reach the installer, and nothing of the
	// installer's may be eaten here.
	for _, unwanted := range []string{"--install", "--quiet", `C:\data`} {
		if contains(theirs, unwanted) {
			t.Fatalf("%q leaked into the installer's arguments: %v", unwanted, theirs)
		}
	}
	if contains(mine, "-TestMode") {
		t.Fatalf("-TestMode is the installer's: %v", mine)
	}
	// Order is preserved, because -Name value pairs depend on it.
	if len(theirs) != 5 || theirs[0] != "-SourceDirectory" || theirs[1] != `C:\candidate` {
		t.Fatalf("the installer's arguments lost their order: %v", theirs)
	}
}

func TestInstallPhasesComeFromTheInstallersOwnLines(t *testing.T) {
	for line, want := range map[string]string{
		"PREFLIGHT COMPLETE":            "preflight",
		"CANDIDATE: Agent_b.exe …":      "checking the candidate",
		"STOPPING Agent_b":              "stopping the running application",
		"INSTALLATION COMPLETE":         "finishing",
		"INSTALLATION FAILED: …":        "failed",
		"something the installer wrote": "",
	} {
		if got := phaseFor(line); got != want {
			t.Fatalf("phaseFor(%q) = %q, want %q", line, got, want)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
