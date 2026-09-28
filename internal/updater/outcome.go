package updater

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Item 2mr (a) and (c): THE INSTALLER'S OWN OUTCOME, READ BACK.
//
// The operator pressed Update on his own client at 02:03 on 2026-09-27, watched it
// say "downloading and verifying", and watched it go back to "update available"
// with no reason. Install reports {"started": true} the moment the setup LAUNCHES
// and never learns what the setup then did, so an installer that ran and REFUSED
// looked exactly like success. The refusal was there in his data root the whole
// time — install-progress.jsonl carried
//
//	{"phase":"failed","text":"INSTALLATION FAILED: WorkspaceDirectory must name a
//	dedicated Agent_b or workspace directory: ...\profiles\acme\scratch"}
//
// and nothing read it. That file is the installer's own voice, written by the
// installer itself so the readout survives the wrapper (item 2gl), so it is the
// right thing to read: no new channel, no guess about what went wrong.
//
// A failure is reported in the installer's WORDS. A success is not reported here
// at all, because a successful install ends this process.
const installProgressName = "install-progress.jsonl"

// InstallOutcomeTimeout bounds the watch. An install that has neither failed nor
// replaced this process within it is left alone: saying nothing is correct, and
// inventing a failure for a slow install would be worse than the silence this
// item exists to end.
const InstallOutcomeTimeout = 3 * time.Minute

type installProgressLine struct {
	Phase string `json:"phase"`
	Text  string `json:"text"`
	Done  bool   `json:"done"`
}

// readInstallFailure returns the installer's own sentence if the progress file
// records a failure at or after the given time, and "" otherwise. A file that
// cannot be read is not a failure: the installer may not have written it yet.
func readInstallFailure(dataRoot string, since time.Time) string {
	file, err := os.Open(filepath.Join(dataRoot, installProgressName))
	if err != nil {
		return ""
	}
	defer file.Close()
	reason := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var line installProgressLine
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if line.Phase != "failed" {
			continue
		}
		// The installer writes two failed lines: its own reason, then the generic
		// "safe to run again". The first one is the one that says anything.
		if text := strings.TrimSpace(line.Text); text != "" && reason == "" {
			reason = text
		}
	}
	return reason
}

// watchInstallOutcome polls for the installer's verdict until it finds a failure
// or the timeout passes. It never blocks Install: a refused install must reach the
// control the operator is looking at, and a successful one replaces this process
// before the watch matters.
//
// Item 2nh (a) and (d): IT ALSO FOLLOWS THE PHASES. The same file that carries the
// verdict carries the sequence, and the operator's complaint was not that the
// update failed — it succeeded — but that nothing showed it happening. Each new
// phase the installer writes becomes the line on the one wait element, so the
// readout is the installer's own account of where it is, not a guess about timing.
func (m *Manager) watchInstallOutcome(started time.Time) {
	deadline := started.Add(InstallOutcomeTimeout)
	seen := ""
	for m.now().Before(deadline) {
		time.Sleep(installOutcomePoll)
		phases := readProgressPhases(m.dataRoot)
		if len(phases) > 0 {
			last := phases[len(phases)-1]
			if last.Phase != seen {
				seen = last.Phase
				if step, line := stepFor(last.Phase); step != "" {
					m.setStep(step, line, 0, 0)
				}
			}
		}
		outcome := outcomeFor(m.dataRoot, m.State().CurrentVersion)
		if outcome == nil {
			continue
		}
		if outcome.OK && outcome.Running == "" {
			continue
		}
		if outcome.OK {
			// Item 2mk (a): AN INSTALL THAT FINISHED WITHOUT REPLACING THIS PROCESS
			// IS NOT A SUCCESS THIS WINDOW CAN CLAIM. outcomeFor only returns an OK
			// for the version this process is running, so reaching here at all means
			// the installer finished and something else is still serving — which is
			// exactly what happened on 2026-09-26: v1.22.0 was written to the
			// per-user root while the window went on being answered by another
			// instance, and nothing said so for eleven releases.
			m.mu.Lock()
			m.state.Step, m.state.Line, m.state.Processed, m.state.Total = "", "", 0, 0
			outcome.ApplicationRoot = m.applicationRoot
			m.state.Outcome = outcome
			state := m.state
			m.mu.Unlock()
			m.publish(state)
			return
		}
		m.mu.Lock()
		// Installing is already false: Install cleared it when the setup launched.
		outcome.ApplicationRoot = m.applicationRoot
		m.state.Error = outcome.Error
		m.state.Step, m.state.Line, m.state.Processed, m.state.Total = "", "", 0, 0
		m.state.Outcome = outcome
		state := m.state
		m.mu.Unlock()
		m.publish(state)
		return
	}
	// The bound passed with no verdict. Item 2nh (a): the stage the wait element is
	// showing is no longer true, and an element that waits forever is worse than
	// none — InstallOutcomeTimeout exists precisely because saying nothing is the
	// honest answer here.
	m.setStep("", "", 0, 0)
}

var installOutcomePoll = 500 * time.Millisecond
