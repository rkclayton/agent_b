package updater

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Item 2nh: THE INSTALLER'S OWN SEQUENCE, READ AS A SEQUENCE.
//
// The operator pressed Update to v1.29.0, it SUCCEEDED, and he reported "update
// failed. this needs to be a more controlled process". Three faults, and all three
// are in what was read rather than in what happened: nothing showed the sequence
// while it ran, the last line of the progress file was a migration warning written
// as a success and read as an error, and the instance that came back had no memory
// of the update at all.
//
// So this file reads the whole progress file rather than hunting one phase in it:
// the phases in order, the warnings separately from the failures, and the finish as
// the outcome the restarted instance can state. It is the installer's own voice
// either way — no new channel, nothing inferred that the installer did not write.
type progressPhase struct {
	At    string `json:"at"`
	Phase string `json:"phase"`
	Text  string `json:"text"`
	Done  bool   `json:"done"`
	OK    bool   `json:"ok"`
}

// Outcome is what the instance that came back after a restart can say about the
// update that replaced it. A success names the version and the time; a failure
// names the phase it stopped in, the installer's own sentence, and the transcript.
type Outcome struct {
	Version    string   `json:"version,omitempty"`
	At         string   `json:"at,omitempty"`
	OK         bool     `json:"ok"`
	Phase      string   `json:"phase,omitempty"`
	Error      string   `json:"error,omitempty"`
	Transcript string   `json:"transcript,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

var installedVersion = regexp.MustCompile(`Agent_b (v?[0-9][0-9A-Za-z.\-+]*) is installed`)

// readProgressPhases returns every line of the progress file in order. A file that
// cannot be read is not a failure: the installer may not have written it yet.
func readProgressPhases(dataRoot string) []progressPhase {
	file, err := os.Open(filepath.Join(dataRoot, installProgressName))
	if err != nil {
		return nil
	}
	defer file.Close()
	phases := []progressPhase{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var line progressPhase
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if strings.TrimSpace(line.Phase) == "" {
			continue
		}
		phases = append(phases, line)
	}
	return phases
}

// warningsIn collects the notes the installer wrote that are not failures. Item
// 2nh (c): the migration-left-in-place line is one of these, and it used to be the
// last line of the file written as a finish, which is how a success came to be read
// as an error.
func warningsIn(phases []progressPhase) []string {
	notes := []string{}
	for _, phase := range phases {
		note := phase.Phase == "warning"
		// The operator's 23:22 file is the reason for the second half of this
		// condition: the build he updated with wrote the migration warning as
		// "finished ok:true", AFTER the real finish, so his successful update ended
		// with "MIGRATION LEFT IN PLACE: access denied" as its last line. A finish
		// that does not say something is installed is a note, whichever build wrote
		// it, and this file is read from installs that predate the fix.
		if phase.Done && phase.OK && !installedVersion.MatchString(phase.Text) {
			note = true
		}
		if !note {
			continue
		}
		if text := strings.TrimSpace(phase.Text); text != "" {
			notes = append(notes, text)
		}
	}
	return notes
}

// transcriptIn pulls the transcript path out of the installer's own failure
// sentence, which ends with "Transcript: <path>" (item 2nf (c)).
func transcriptIn(text string) string {
	marker := "Transcript: "
	index := strings.LastIndex(text, marker)
	if index < 0 {
		return ""
	}
	return strings.TrimSpace(text[index+len(marker):])
}

// outcomeFor reads the progress file and states what the last install did, or nil
// when the file records nothing finished. A success is reported only when the
// version it finished with is the version now running: a finish for some other
// version is somebody else's install, and claiming it would be a lie about this
// process.
func outcomeFor(dataRoot, currentVersion string) *Outcome {
	phases := readProgressPhases(dataRoot)
	if len(phases) == 0 {
		return nil
	}
	last := phases[len(phases)-1]
	for index := len(phases) - 1; index >= 0; index-- {
		if phases[index].Done {
			last = phases[index]
			break
		}
	}
	if !last.Done {
		return nil
	}
	if !last.OK {
		return &Outcome{
			Version:    installedVersionIn(phases),
			At:         last.At,
			OK:         false,
			Phase:      last.Phase,
			Error:      strings.TrimSpace(last.Text),
			Transcript: transcriptIn(last.Text),
			Warnings:   warningsIn(phases),
		}
	}
	version := normalizeVersion(installedVersionIn(phases))
	if version == "" || version != normalizeVersion(currentVersion) {
		return nil
	}
	return &Outcome{Version: version, At: last.At, OK: true, Warnings: warningsIn(phases)}
}

// installedVersionIn reads the version out of the installer's own sentences rather
// than out of a second record that could disagree with them.
func installedVersionIn(phases []progressPhase) string {
	for index := len(phases) - 1; index >= 0; index-- {
		if match := installedVersion.FindStringSubmatch(phases[index].Text); match != nil {
			return match[1]
		}
	}
	for _, phase := range phases {
		if phase.Phase == "starting" || phase.Phase == "preflight" {
			if fields := strings.Fields(phase.Text); len(fields) > 0 {
				if candidate := fields[len(fields)-1]; strings.HasPrefix(candidate, "v") {
					return candidate
				}
			}
		}
	}
	return ""
}

// stepFor maps one of the installer's phase names onto the step the wait element
// names while it is in hand. An unknown phase keeps its own name: the installer is
// allowed to say something this build has never heard of.
func stepFor(phase string) (string, string) {
	switch phase {
	case "starting", "preflight":
		return "installing", "checking the installation before anything is changed"
	case "copying the application":
		return "installing", "copying the application"
	case "stopping the running application":
		return "stopping", "stopping the app — the window will close and reopen"
	case "seeding the configuration":
		return "installing", "seeding the configuration"
	case "restarting":
		return "restarting", "starting the new version"
	case "finished":
		return "restarting", "finishing"
	case "warning":
		return "", ""
	}
	return "installing", phase
}
