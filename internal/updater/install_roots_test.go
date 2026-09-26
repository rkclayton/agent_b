package updater

import (
	"strings"
	"testing"
)

// Item 2lh (a) and (e). rel-1.13.2/W8 could not exercise the install launch on a
// disposable instance, because the setup was invoked with `--install --quiet` and
// no roots, and `Resolve-AgentBInstallRoots` then defaults to the operator's own
// `<LocalAppData>\Agent_b` and `<LocalAppData>\Programs\Agent_b` from a shell
// folder, which an inherited environment cannot redirect. So any instance that
// asked for an update installed over production.
func TestTheSetupIsToldWhereTheAskingInstanceLives2lh(t *testing.T) {
	arguments, err := installArguments(`C:\suite\root\Application\Agent_b`, `C:\suite\root\Data\Agent_b`, `C:\suite\root\workspace`, "")
	if err != nil {
		t.Fatalf("a disposable instance with roots refused to install: %v", err)
	}
	joined := strings.Join(arguments, " ")
	for _, want := range []string{
		"--install", "--quiet",
		`-ApplicationDirectory C:\suite\root\Application\Agent_b`,
		`-DataDirectory C:\suite\root\Data\Agent_b`,
		`-WorkspaceDirectory C:\suite\root\workspace`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the setup was not told %q: %s", want, joined)
		}
	}
	// The reopen-session argument the operator's own update relies on is still passed.
	withSession, err := installArguments(`C:\a`, `C:\d`, "", "s29")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(withSession, " "), "--reopen-session s29") {
		t.Fatalf("the chat to reopen was dropped: %v", withSession)
	}
}

// (b): an instance that cannot name its own roots refuses, rather than falling
// back to the operator's location. That fallback is the whole defect.
func TestAnInstanceThatCannotNameItsRootsRefuses2lh(t *testing.T) {
	for _, roots := range [][2]string{{"", `C:\d`}, {`C:\a`, ""}, {"", ""}, {"   ", `C:\d`}} {
		arguments, err := installArguments(roots[0], roots[1], "", "")
		if err == nil {
			t.Fatalf("an instance with application=%q data=%q installed anyway: %v", roots[0], roots[1], arguments)
		}
		for _, want := range []string{"cannot name its own", "will not install", "by hand"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the refusal does not say %q: %v", want, err)
			}
		}
	}
}

// Item 2ll (d): both arguments carry the same root. -DataDirectory is the
// installer script's and --install-data is the process's; rel-1.14.0 passed only
// the first, so the install's own log, marker and progress file still resolved
// to the operator's LocalAppData whatever instance asked for the update.
func TestBothDataArgumentsCarryTheSameRoot2ll(t *testing.T) {
	const data = `C:\suite\root\Data\Agent_b`
	arguments, err := installArguments(`C:\suite\root\Application\Agent_b`, data, `C:\suite\root\workspace`, "")
	if err != nil {
		t.Fatalf("installArguments: %v", err)
	}
	roots := map[string]string{}
	for index := 0; index+1 < len(arguments); index++ {
		switch arguments[index] {
		case "--install-data", "-DataDirectory":
			roots[arguments[index]] = arguments[index+1]
		}
	}
	if len(roots) != 2 {
		t.Fatalf("both data arguments are not present: %v", arguments)
	}
	if roots["--install-data"] != data || roots["-DataDirectory"] != data {
		t.Fatalf("the two data arguments disagree: %v", roots)
	}
}
