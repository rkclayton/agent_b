//go:build windows

package serviceaccount

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Item 2kk (f). The operator took Repair on v1.11.0 and got this back:
//
//	elevated service-account setup failed: #< CLIXML AGENTB_ELEVATED_STARTED
//	<Objs …><Obj S="progress" …><AV>Preparing modules for first use.</AV>…
//
// truncated at "the elevated". Those are PROGRESS RECORDS. A Windows PowerShell
// 5.1 child serializes them to stderr as CLIXML, the harness read anything on
// that stream as failure text, and the real outcome was lost. These fixtures
// stand in for that child: what matters is that noise on a stream is not an
// outcome, and that the outcome comes from the exit code and a file.

// fixtureManager builds a manager whose "elevated" child is a script we control,
// writing into a disposable directory.
func fixtureManager(t *testing.T, script string) *windowsManager {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.ps1")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	manager, ok := New(path).(*windowsManager)
	if !ok {
		t.Fatal("New did not return a windowsManager")
	}
	manager.workDir = dir
	// The fixture child runs directly, not elevated. A test must never reach
	// Start-Process -Verb RunAs: on this machine that raises a Windows credential
	// prompt on the operator own desktop, which is a hard stop. The launcher
	// prints the same started marker the real one does, so everything downstream
	// of the launch -- the exit code, the result file, the kept streams -- is
	// exercised exactly as in production.
	manager.runLauncher = func(ctx context.Context, arguments []string) ([]byte, error) {
		child := exec.CommandContext(ctx, manager.powershell,
			append([]string{"-NoLogo", "-NoProfile", "-NonInteractive"}, arguments...)...)
		output, err := child.CombinedOutput()
		return append([]byte("AGENTB_ELEVATED_STARTED\n"), output...), err
	}
	return manager
}

// The CLIXML fixture: it emits exactly the shape that broke provisioning, writes
// a result saying it succeeded, and exits 0.
const clixmlSuccessFixture = `
param([string]$AccountName, [string]$CredentialStore, [switch]$NoPrompt, [switch]$ResetPassword, [string]$ResultFile)
# The noise the operator actually saw, on the stream the harness used to read.
[Console]::Error.WriteLine('#< CLIXML')
[Console]::Error.WriteLine('<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04"><Obj S="progress" RefId="0"><TN RefId="0"><T>System.Management.Automation.PSCustomObject</T></TN><MS><I64 N="SourceId">1</I64><PR N="Record"><AV>Preparing modules for first use.</AV></PR></MS></Obj></Objs>')
if ($ResultFile) {
  [IO.File]::WriteAllText($ResultFile, '{"ok":true,"message":"the service identity is ready","outcome":"ready","account":"agentb-svc"}')
}
exit 0
`

func TestCLIXMLProgressIsNotFailure2kk(t *testing.T) {
	manager := fixtureManager(t, clixmlSuccessFixture)
	result, err := manager.Setup(context.Background(), "agentb-svc", writeCredential(t), false, nil)
	if err != nil {
		t.Fatalf("a child that emitted CLIXML progress and exited 0 was read as a failure: %v", err)
	}
	if result.Launch != LaunchStarted {
		t.Fatalf("launch = %q, want started", result.Launch)
	}
	if result.Result == nil || !result.Result.Ok {
		t.Fatal("the child's own result was not read")
	}
	if result.Result.Message != "the service identity is ready" {
		t.Fatalf("message = %q, want the child's own", result.Result.Message)
	}
	// (c): the launcher's streams are KEPT, and they are not the outcome.
	//
	// Note what this proves and what it cannot. The ELEVATED GRANDCHILD's streams
	// are not captured at all -- Start-Process -WindowStyle Hidden redirects
	// nothing -- so the CLIXML this fixture writes goes nowhere. The CLIXML the
	// operator saw on v1.11.0 came from the LAUNCHER, whose CombinedOutput the Go
	// side reads. That asymmetry is the whole reason (a) needs a file: there is no
	// stream from the child to read, only its exit code and what it writes down.
	kept, readErr := os.ReadFile(result.LogPath)
	if readErr != nil {
		t.Fatalf("the launcher's streams were not kept: %v", readErr)
	}
	if !strings.Contains(string(kept), "AGENTB_ELEVATED_STARTED") {
		t.Fatalf("the log does not carry the launcher's own output: %q", string(kept))
	}
	if strings.Contains(err2String(result), "CLIXML") {
		t.Fatal("raw CLIXML reached the caller")
	}
}

// A child that fails writes why, and that message is what comes back — whole.
const failingFixture = `
param([string]$AccountName, [string]$CredentialStore, [switch]$NoPrompt, [switch]$ResetPassword, [string]$ResultFile)
[Console]::Error.WriteLine('#< CLIXML')
[Console]::Error.WriteLine('<Objs><Obj S="progress"><AV>Preparing modules for first use.</AV></Obj></Objs>')
if ($ResultFile) {
  [IO.File]::WriteAllText($ResultFile, '{"ok":false,"message":"the account exists and belongs to Administrators, so it will not be adopted","outcome":"refused"}')
}
exit 3
`

func TestAFailingChildReportsItsOwnMessage2kk(t *testing.T) {
	manager := fixtureManager(t, failingFixture)
	result, err := manager.Setup(context.Background(), "agentb-svc", writeCredential(t), false, nil)
	if err == nil {
		t.Fatal("a child that exited 3 was read as success")
	}
	const want = "the account exists and belongs to Administrators, so it will not be adopted"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("the child's message did not survive: %v", err)
	}
	// (d): complete, never truncated, and never the raw stream.
	if strings.Contains(err.Error(), "CLIXML") {
		t.Fatalf("raw CLIXML reached the operator: %v", err)
	}
	if !strings.Contains(err.Error(), result.LogPath) {
		t.Fatalf("the message does not name where the full output is: %v", err)
	}
	if result.Launch != LaunchStarted {
		t.Fatalf("launch = %q, want started", result.Launch)
	}
	if !result.Attempted {
		t.Fatal("a child that ran and failed must report that it was attempted")
	}
}

// (e): a launch that never happened is its own state. The fixture never prints
// the started marker, which is what a declined UAC looks like from here.
const declinedFixture = `
param([string]$AccountName, [string]$CredentialStore, [switch]$NoPrompt, [switch]$ResetPassword, [string]$ResultFile)
exit 0
`

func TestADeclinedLaunchIsItsOwnState2kk(t *testing.T) {
	manager := fixtureManager(t, declinedFixture)
	// What a declined UAC looks like from here: the launcher ran, said the
	// elevation did not start, and exited non-zero without ever reaching a child.
	manager.runLauncher = func(context.Context, []string) ([]byte, error) {
		return []byte("AGENTB_ELEVATION_NOT_STARTED\n"), errors.New("exit status 1")
	}
	result, err := manager.Setup(context.Background(), "agentb-svc", writeCredential(t), false, nil)
	if err == nil {
		t.Fatal("a launch that never started was read as success")
	}
	if result.Launch != LaunchDeclined {
		t.Fatalf("launch = %q, want declined", result.Launch)
	}
	if result.Attempted {
		t.Fatal("nothing was attempted, so nothing may be reported as attempted")
	}
	if !strings.Contains(err.Error(), "no account operation ran") {
		t.Fatalf("a declined launch did not say that nothing ran: %v", err)
	}
}

// A child that writes no result at all still reports honestly from its exit code.
const silentFailureFixture = `
param([string]$AccountName, [string]$CredentialStore, [switch]$NoPrompt, [switch]$ResetPassword, [string]$ResultFile)
exit 9
`

func TestASilentFailureStillReportsFromTheExitCode2kk(t *testing.T) {
	manager := fixtureManager(t, silentFailureFixture)
	_, err := manager.Setup(context.Background(), "agentb-svc", writeCredential(t), false, nil)
	if err == nil {
		t.Fatal("a child that exited 9 was read as success")
	}
	if !strings.Contains(err.Error(), "did not complete") {
		t.Fatalf("a silent failure did not fall back to the caller's own words: %v", err)
	}
}

func TestTheResultFileIsReadAsWritten2kk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	payload := ElevatedResult{Ok: true, Message: "ready", Outcome: "ready", Account: "agentb-svc"}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readElevatedResult(path); got == nil || got.Message != "ready" || !got.Ok {
		t.Fatalf("readElevatedResult = %+v", got)
	}
	// Absent, unreadable and empty-message results are all "no result", never an
	// error of their own: the exit code decides.
	if got := readElevatedResult(filepath.Join(dir, "absent.json")); got != nil {
		t.Fatal("a missing result file became a result")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readElevatedResult(path); got != nil {
		t.Fatal("an unreadable result file became a result")
	}
}

func writeCredential(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credential.dpapi")
	if err := os.WriteFile(path, []byte("not a real blob"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// err2String renders what a caller would surface for a successful run, so a test
// can assert that nothing raw leaks into it.
func err2String(result SetupResult) string {
	if result.Result == nil {
		return ""
	}
	return result.Result.Message
}

// Item 2ng (b): THE CHILD'S OWN FIRST COMPLAINT, out of the log the wrapper captured.
//
// Before this, a child that died above the script's result writer left the operator with
// "the elevated service-account setup did not complete" and a 25-byte log holding one
// marker line. The wrapper captures the streams; this is what turns them into the one
// line worth showing.
func TestTheFirstErrorLineIsTheReasonAndNotTheNoiseAroundIt(t *testing.T) {
	directory := t.TempDir()
	log := filepath.Join(directory, "service-identity.log")
	// A real PowerShell failure, in shape: the marker lines the wrapper writes, then the
	// error, then its continuation block, then the launcher's own tail.
	body := strings.Join([]string{
		"AGENTB_ELEVATED_WRAPPER_STARTED 2026-09-27T19:44:25.9100000Z",
		"C:\\path\\provision-service-identity.ps1 : Cannot validate argument on parameter 'ModelPort'.",
		"At line:1 char:1",
		"+ & 'C:\\path\\provision-service-identity.ps1' -ModelPort 0",
		"+ ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~",
		"    + CategoryInfo          : InvalidData: (:) [], ParameterBindingValidationException",
		"AGENTB_ELEVATED_WRAPPER_EXIT 1",
		"AGENTB_LAUNCHER_OUTPUT",
		"AGENTB_ELEVATED_STARTED",
	}, "\r\n")
	if err := os.WriteFile(log, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	reason := firstErrorLine(log)
	if !strings.Contains(reason, "Cannot validate argument on parameter 'ModelPort'") {
		t.Fatalf("the reason is %q", reason)
	}
	// None of the surrounding block is the reason.
	for _, noise := range []string{"AGENTB_", "At line", "CategoryInfo", "~~~"} {
		if strings.Contains(reason, noise) {
			t.Errorf("the reason carries %q: %q", noise, reason)
		}
	}
}

func TestALogWithNothingButMarkersHasNoReasonRatherThanAGuess(t *testing.T) {
	directory := t.TempDir()
	// Exactly what the operator's own failed run left behind.
	log := filepath.Join(directory, "his.log")
	if err := os.WriteFile(log, []byte("AGENTB_ELEVATED_STARTED\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason := firstErrorLine(log); reason != "" {
		t.Errorf("a log with only a marker produced %q", reason)
	}
	if reason := firstErrorLine(filepath.Join(directory, "absent.log")); reason != "" {
		t.Errorf("a missing log produced %q", reason)
	}
	// CLIXML noise is not a reason either: item 2kk exists because it was read as one.
	clixml := filepath.Join(directory, "clixml.log")
	if err := os.WriteFile(clixml, []byte("#< CLIXML\r\n<Objs Version=\"1.1.0.1\">\r\n<S S=\"progress\">x</S>\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason := firstErrorLine(clixml); reason != "" {
		t.Errorf("CLIXML was read as a reason: %q", reason)
	}
}

// (a): the wrapper is what gets elevated, and the inner call is given the script's own
// arguments once — not the host flags twice.
func TestTheElevatedArgumentsAreStrippedToTheScriptsOwn(t *testing.T) {
	script := "C:\\app\\scripts\\provision-service-identity.ps1"
	full := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-File", script, "-AccountName", "agentb-svc", "-ModelPort", "8000"}
	stripped := stripLeadingHostArguments(full, script)
	for _, gone := range []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-File", script} {
		for _, argument := range stripped {
			if argument == gone {
				t.Errorf("%q survived the strip: %v", gone, stripped)
			}
		}
	}
	if len(stripped) != 4 || stripped[0] != "-AccountName" || stripped[3] != "8000" {
		t.Fatalf("the script's own arguments were not kept in order: %v", stripped)
	}
	// A list that does not name this script is returned whole rather than truncated.
	other := stripLeadingHostArguments([]string{"-AccountName", "x"}, script)
	if len(other) != 2 {
		t.Errorf("an unrecognised list was altered: %v", other)
	}
}

// Item 2ng (a) and (b) TOGETHER, END TO END: A FAILURE ABOVE THE SCRIPT'S OWN WRITER IS
// NOW READABLE.
//
// This is the case that beat the operator on 2026-09-27 at 19:44. The elevated child
// exited nonzero without writing a result file, because whatever failed happened above
// the code that writes one — and because `Start-Process -Verb RunAs -WindowStyle Hidden`
// redirects nothing, the reason went to a console nobody could see. All he got was a
// 25-byte log holding one marker line and the sentence "the elevated service-account
// setup did not complete".
//
// The wrapper is the real one, copied beside the fixture so the manager finds it exactly
// as it finds it in an installation. Nothing here elevates: the fixture seam runs the
// child directly, because reaching Start-Process -Verb RunAs would raise a credential
// prompt on the operator's own desktop, which is a hard stop rather than a test.
const preWriterFailureFixture = `
param([string]$AccountName, [string]$CredentialStore, [switch]$NoPrompt, [switch]$ResetPassword, [string]$ResultFile)
# A failure ABOVE the writer: the script throws before it can record anything, exactly as
# a parameter that will not bind or a policy refusal does.
throw 'Administrator elevation is required to provision the Agent_b service identity.'
`

func TestAFailureAboveTheResultWriterIsCapturedAndQuoted2ng(t *testing.T) {
	manager := fixtureManager(t, preWriterFailureFixture)
	// The real wrapper, beside the fixture, where the manager looks for it.
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-elevated-provision.ps1"))
	if err != nil {
		t.Fatalf("read the wrapper: %v", err)
	}
	wrapper := filepath.Join(filepath.Dir(manager.scriptPath), "run-elevated-provision.ps1")
	if err := os.WriteFile(wrapper, source, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := manager.Setup(context.Background(), "agentb-svc", writeCredential(t), false, nil)
	if err == nil {
		t.Fatal("a child that threw above its writer was reported as a success")
	}
	if result.Launch != LaunchStarted {
		t.Fatalf("launch = %q, want started", result.Launch)
	}
	if result.Result != nil {
		t.Fatalf("a result file appeared where the fixture writes none: %+v", result.Result)
	}

	// (b): the message is the child's OWN first line, not the generic sentence.
	if strings.Contains(err.Error(), "did not complete") {
		t.Errorf("the generic sentence is still what the operator would see: %v", err)
	}
	if !strings.Contains(err.Error(), "Administrator elevation is required") {
		t.Errorf("the child's own reason did not reach the message: %v", err)
	}
	// The log path is still named, because the first line is a summary and the file is
	// the account.
	if !strings.Contains(err.Error(), result.LogPath) {
		t.Errorf("the message does not name the log: %v", err)
	}

	// (a): the log holds the child's streams AND the launcher's, and the wrapper's own
	// markers show it ran. This is what the 25-byte log could not do.
	captured, readErr := os.ReadFile(result.LogPath)
	if readErr != nil {
		t.Fatalf("read the captured log: %v", readErr)
	}
	for _, wanted := range []string{
		"AGENTB_ELEVATED_WRAPPER_STARTED",
		"Administrator elevation is required",
		"AGENTB_ELEVATED_WRAPPER_EXIT",
		// The launcher's own streams are APPENDED rather than overwriting the capture,
		// which is the mistake that would have made the wrapper pointless.
		"AGENTB_LAUNCHER_OUTPUT",
		"AGENTB_ELEVATED_STARTED",
	} {
		if !strings.Contains(string(captured), wanted) {
			t.Errorf("the log is missing %q:\n%s", wanted, captured)
		}
	}
	if len(captured) < 200 {
		t.Errorf("the log is %d bytes, which is the size that told him nothing", len(captured))
	}
}
