package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// lifetime records how the serving process ends in the launcher log, the same
// file the launcher appends to. A detached launcher has already exited, so
// before v0.64.0 nothing recorded an exit at all: production vanished at two
// Fast Startup shutdowns (a logoff) with no line anywhere (item 2em). An exit
// the process observes is recorded as it happens; one it cannot observe (a
// forced termination, power loss) is recorded at the next start from the run
// marker the earlier process left behind.
type lifetime struct {
	logPath         string
	markerPath      string
	pid             int
	created         int64
	now             func() time.Time
	once            sync.Once
	applicationRoot string
	listen          string
}

type runMarker struct {
	PID         int    `json:"pid"`
	Created     int64  `json:"created"`
	Started     string `json:"started"`
	Application string `json:"application,omitempty"`
	Listen      string `json:"listen,omitempty"`
}

const launcherLogName = "launcher-errors.log"

func newLifetime(dataRoot string, now func() time.Time) *lifetime {
	logs := filepath.Join(dataRoot, "logs")
	return &lifetime{logPath: filepath.Join(logs, launcherLogName), markerPath: filepath.Join(dataRoot, "agent_b-run.json"), pid: os.Getpid(), created: processCreated(os.Getpid()), now: now}
}

// begin runs once the listener is bound, so a second instance that cannot bind
// never takes over the running instance's marker.
func (l *lifetime) begin() {
	if err := os.MkdirAll(filepath.Dir(l.logPath), 0o700); err != nil {
		return
	}
	if data, err := readMarker(l.markerPath); err == nil {
		var previous runMarker
		if json.Unmarshal(data, &previous) == nil && previous.PID > 0 && !(previous.PID == l.pid && previous.Created == l.created) && !processRunning(previous.PID, previous.Created) {
			l.append(fmt.Sprintf("Agent_b PID %d (started %s) ended without recording a reason: the Windows session was logged off or shut down, the process was ended from outside, or the host lost power.", previous.PID, printable(previous.Started)))
		}
	}
	marker, _ := json.Marshal(runMarker{PID: l.pid, Created: l.created, Started: l.stamp(), Application: l.applicationRoot, Listen: l.listen})
	temporary := l.markerPath + ".tmp"
	if os.WriteFile(temporary, marker, 0o600) == nil {
		_ = os.Rename(temporary, l.markerPath)
	}
}

// stopped records the first observed reason and clears the marker; later
// calls (a session end followed by the signal it causes) are ignored.
func (l *lifetime) stopped(reason string) {
	l.once.Do(func() {
		l.append(fmt.Sprintf("Agent_b PID %d stopped: %s", l.pid, strings.TrimSpace(reason)))
		_ = os.Remove(l.markerPath)
	})
}

func (l *lifetime) stamp() string {
	return l.now().Format("2006-01-02 15:04:05 -07:00")
}

func (l *lifetime) append(message string) {
	file, err := os.OpenFile(l.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = fmt.Fprintf(file, "%s %s\r\n", l.stamp(), message)
	_ = file.Sync()
}

func appendLauncherMessage(dataRoot, message string) {
	life := newLifetime(dataRoot, time.Now)
	life.append(message)
}

// readMarker reads at most 4 KiB of the run marker: it is a few fields, and a
// larger file is not one this process wrote (v0.64.0/W8).
func readMarker(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, 4096))
}

// printable drops control characters, so a marker's text cannot start a
// second line in the launcher log.
func printable(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
}
