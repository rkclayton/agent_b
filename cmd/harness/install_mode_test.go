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
	t.Setenv("AGENTB_UPDATE_SOURCE_URL", "")
	t.Setenv("AGENTB_UPDATE_FIXTURE_URL", "http://127.0.0.1:4321/latest")
	if got := updateLatestURL(); got != "http://127.0.0.1:4321/latest" {
		t.Fatalf("loopback fixture URL=%q", got)
	}
	t.Setenv("AGENTB_UPDATE_FIXTURE_URL", "https://example.com/latest")
	if got := updateLatestURL(); got != updater.LatestReleaseURL {
		t.Fatalf("public override was accepted: %q", got)
	}
}

func TestOrganizationUpdateSourceRequiresHTTPS(t *testing.T) {
	t.Setenv("AGENTB_UPDATE_FIXTURE_URL", "")
	t.Setenv("AGENTB_UPDATE_SOURCE_URL", "https://updates.example.test/releases/latest")
	if got := updateLatestURL(); got != "https://updates.example.test/releases/latest" {
		t.Fatalf("organization source URL=%q", got)
	}
	t.Setenv("AGENTB_UPDATE_SOURCE_URL", "http://updates.example.test/releases/latest")
	if got := updateLatestURL(); got != updater.LatestReleaseURL {
		t.Fatalf("insecure organization source was accepted: %q", got)
	}
}

func TestExecutionPolicyPrecedenceAndBlockedState(t *testing.T) {
	got := resolveExecutionPolicy(map[string]string{
		"LocalMachine": "RemoteSigned", "CurrentUser": "Unrestricted",
		"UserPolicy": "AllSigned", "MachinePolicy": "Restricted",
	})
	if got.Policy != "Restricted" || got.Scope != "MachinePolicy" || !got.BlocksScripts {
		t.Fatalf("effective policy=%+v", got)
	}
	got = resolveExecutionPolicy(map[string]string{"LocalMachine": "RemoteSigned", "Process": "Bypass"})
	if got.Policy != "Bypass" || got.Scope != "Process" || got.BlocksScripts {
		t.Fatalf("process policy=%+v", got)
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

func TestOuterSignatureIsReportedAndOnlyHashMismatchIsRefused2ox(t *testing.T) {
	for _, test := range []struct {
		status, subject, line string
		refused               bool
	}{
		{"Valid", "CN=Organisation", "outer: Valid CN=Organisation", false},
		{"UnknownError", "CN=Agent_b Operator Code Signing", "outer: UnknownError CN=Agent_b Operator Code Signing", false},
		{"NotSigned", "", "outer: NotSigned none", false},
		{"HashMismatch", "CN=Agent_b Operator Code Signing", "outer: HashMismatch CN=Agent_b Operator Code Signing", true},
	} {
		line, err := outerSignatureDecision(test.status, test.subject)
		if line != test.line || (err != nil) != test.refused {
			t.Fatalf("%s/%s: line=%q err=%v", test.status, test.subject, line, err)
		}
	}
}

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
	if err := clearInstallMarker(root); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := readInstallMarker(root); found {
		t.Fatal("the marker outlived the install that cleared it")
	}
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
	if bare := describeInterruptedInstall(InstallMarker{}); len(bare) < 40 {
		t.Fatalf("an empty marker still gets a sentence: %q", bare)
	}
}

func TestInstallArgumentsSplitByWhoOwnsThem(t *testing.T) {
	arguments := []string{
		"--install", "--quiet", "--all-users", "--install-data", `C:\data`, "--reopen-session", "s17", "-NoStart",
		"-SourceDirectory", `C:\candidate`, "-TestMode",
		"-UninstallRegistryPath", `HKCU:\Software\X\Agent_b`,
	}
	mine := installFlagArgs(arguments)
	theirs := installPassthrough(arguments)
	for _, want := range []string{"--install", "--quiet", "--all-users", "--install-data", `C:\data`, "--reopen-session", "s17", "-NoStart"} {
		if !contains(mine, want) {
			t.Fatalf("this mode keeps %q: %v", want, mine)
		}
	}
	for _, want := range []string{"-SourceDirectory", `C:\candidate`, "-TestMode", "-UninstallRegistryPath", `HKCU:\Software\X\Agent_b`} {
		if !contains(theirs, want) {
			t.Fatalf("the installer keeps %q: %v", want, theirs)
		}
	}
	for _, unwanted := range []string{"--install", "--quiet", `C:\data`} {
		if contains(theirs, unwanted) {
			t.Fatalf("%q leaked into the installer's arguments: %v", unwanted, theirs)
		}
	}
	if contains(mine, "-TestMode") {
		t.Fatalf("-TestMode is the installer's: %v", mine)
	}
	if len(theirs) != 5 || theirs[0] != "-SourceDirectory" || theirs[1] != `C:\candidate` {
		t.Fatalf("the installer's arguments lost their order: %v", theirs)
	}
}

func TestInstallLaunchPathsComeFromInstallerArguments(t *testing.T) {
	arguments := []string{"-ApplicationDirectory", `C:\Program Files\Agent_b test`, `-DataDirectory=C:\Agent_b data`}
	if got := installerArgument(arguments, "applicationdirectory", "fallback"); got != `C:\Program Files\Agent_b test` {
		t.Fatalf("application=%q", got)
	}
	if got := installerArgument(arguments, "DataDirectory", "fallback"); got != `C:\Agent_b data` {
		t.Fatalf("data=%q", got)
	}
}

func TestInstallFailureRestartDetailsComeFromTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installer.log")
	if err := os.WriteFile(path, []byte("ROLLBACK: restored files\nRESTART VERSION: v1.6.1\nRESTART REASON: verification failure\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	version, reason, restart := installRestartDetails(path)
	if !restart || version != "v1.6.1" || reason != "verification failure" {
		t.Fatalf("version=%q reason=%q restart=%t", version, reason, restart)
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

func TestTheInstallsRecordsFollowTheAskingInstance2ll(t *testing.T) {
	operator := filepath.Join(os.Getenv("LOCALAPPDATA"), "Agent_b")
	for _, testCase := range []struct {
		name     string
		explicit string
		args     []string
		want     string
	}{
		{"an explicit --install-data wins", `C:\suite\Data\Agent_b`, []string{"-DataDirectory", `C:\other`}, `C:\suite\Data\Agent_b`},
		{"otherwise the installer's own -DataDirectory", "", []string{"-DataDirectory", `C:\suite\Data\Agent_b`}, `C:\suite\Data\Agent_b`},
		{"production, which passes its own root", "", []string{"-DataDirectory", operator}, operator},
		{"and nothing passed is still the operator's location", "", nil, operator},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := installDataRoot(testCase.explicit, testCase.args); got != testCase.want {
				t.Fatalf("installDataRoot = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestADisposableInstallIsHeadlessByConstruction2m6(t *testing.T) {
	t.Setenv("AGENT_B_INSTALL_NO_BROWSER", "")
	for _, probe := range []struct {
		name     string
		root     string
		headless bool
	}{
		{"a disposable test root", filepath.Join(t.TempDir(), "Agent_b"), true},
		{"a temp root that merely ends in the right name", `C:\Temp\Agent_b-installer-test-abc\Application`, true},
		{"the canonical per-user root", defaultInstallRoot(false), false},
		{"the canonical all-users root", defaultInstallRoot(true), false},
	} {
		if got := !canonicalInstallRoot(probe.root); got != probe.headless {
			t.Errorf("%s: headless=%v, want %v (root %q)", probe.name, got, probe.headless, probe.root)
		}
	}
}

func TestARealInstallStillOpensItsWindow2m6(t *testing.T) {
	t.Setenv("AGENT_B_INSTALL_NO_BROWSER", "")
	if !canonicalInstallRoot(defaultInstallRoot(false)) {
		t.Fatal("the canonical per-user root was treated as disposable, which would silence a real install")
	}
	t.Setenv("AGENT_B_INSTALL_NO_BROWSER", "1")
	if os.Getenv("AGENT_B_INSTALL_NO_BROWSER") == "" {
		t.Fatal("the override was not readable")
	}
}

func TestInstallIdentityIsTheTokenSIDAndNeverANameLookup2or(t *testing.T) {
	lookupCalled := false
	got, err := installOperatorSID(
		func() (string, error) { return "S-1-5-21-100", nil },
		func() (string, error) { lookupCalled = true; return "S-1-12-1-200", nil },
	)
	if err != nil || got != "S-1-5-21-100" || lookupCalled {
		t.Fatalf("operator identity = %q, %v; name lookup called=%v", got, err, lookupCalled)
	}

	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install-Agent_b.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(source, []byte("SetOwner")) {
		t.Fatal("the installer still changes directory ownership")
	}
}

func TestPerUserInstallIsNativeAndComplete2or(t *testing.T) {
	source, application, data := t.TempDir(), t.TempDir(), t.TempDir()
	for _, directory := range []string{"web", "prompts", "scripts", "docs"} {
		if err := os.MkdirAll(filepath.Join(source, directory), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, directory, directory+".txt"), []byte(directory), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "scripts", "launch-installed.cmd"), []byte("launcher"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Agent_b.exe", "agentb.exe", "WebView2Loader.dll", "harness.example.json", "SECURITY.md", "LICENSE", "NOTICE"} {
		body := []byte(name)
		if name == "harness.example.json" {
			body = []byte(`{"workspace":"","log_dir":"","memory":{"dir":""}}`)
		}
		if err := os.WriteFile(filepath.Join(source, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	links, registration := 0, map[string]any{}
	plan := nativeInstallPlan{Source: source, Application: application, Data: data, Workspace: filepath.Join(data, "scratch"), StartMenu: filepath.Join(data, "start"), SendTo: filepath.Join(data, "sendto"), Version: "1.50.0", OperatorSID: "S-1-5-21-100"}
	platform := nativeInstallPlatform{
		shortcut: func(shortcutSpec) error { links++; return nil },
		register: func(values map[string]any) error { registration = values; return nil },
	}
	if err := installPerUserNative(plan, platform); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"web/web.txt", "prompts/prompts.txt", "scripts/scripts.txt", "docs/docs.txt", "Agent_b.exe", "agentb.exe", "WebView2Loader.dll"} {
		if _, err := os.Stat(filepath.Join(application, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("missing %s: %v", relative, err)
		}
	}
	if links != 2 || registration["DisplayVersion"] != "1.50.0" || registration["OperatorSid"] != plan.OperatorSID {
		t.Fatalf("links=%d registration=%v", links, registration)
	}
	config, err := os.ReadFile(filepath.Join(data, "harness.json"))
	var decoded map[string]any
	if err != nil || json.Unmarshal(config, &decoded) != nil || decoded["workspace"] != filepath.Join(data, "scratch") {
		t.Fatalf("native config=%s err=%v", config, err)
	}
}

func TestNativeUninstallPreservesDataUnlessPurgeIsRequested2or(t *testing.T) {
	root := t.TempDir()
	plan := nativeInstallPlan{Application: filepath.Join(root, "app"), Data: filepath.Join(root, "data"), StartMenu: filepath.Join(root, "start"), SendTo: filepath.Join(root, "send")}
	for _, path := range []string{filepath.Join(plan.Application, "Agent_b.exe"), filepath.Join(plan.Data, "harness.json"), filepath.Join(plan.StartMenu, "Agent_b.lnk"), filepath.Join(plan.SendTo, "Agent_b.lnk")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unregistered := false
	if err := uninstallPerUserNative(plan, false, func() error { unregistered = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !unregistered {
		t.Fatal("uninstall registration was not removed")
	}
	if _, err := os.Stat(plan.Application); !os.IsNotExist(err) {
		t.Fatalf("application remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plan.Data, "harness.json")); err != nil {
		t.Fatalf("data was not preserved: %v", err)
	}
	if err := uninstallPerUserNative(plan, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plan.Data); !os.IsNotExist(err) {
		t.Fatalf("purged data remains: %v", err)
	}
}
