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
	// unclean is item 2q7 (a): the last instance ended without recording a reason.
	unclean      bool
	previousExit string
	started      time.Time
	onExit       func(string, int64)
}

type runMarker struct {
	PID         int    `json:"pid"`
	Created     int64  `json:"created"`
	Started     string `json:"started"`
	Application string `json:"application,omitempty"`
	Listen      string `json:"listen,omitempty"`
	ExitCause   string `json:"exit_cause,omitempty"`
	UptimeS     int64  `json:"uptime_s,omitempty"`
	Ended       string `json:"ended,omitempty"`
}

const launcherLogName = "launcher-errors.log"

var activeLifetime struct {
	sync.RWMutex
	value *lifetime
}

func setActiveLifetime(life *lifetime) {
	activeLifetime.Lock()
	activeLifetime.value = life
	activeLifetime.Unlock()
}

func stopActiveLifetime(cause, detail string) bool {
	activeLifetime.RLock()
	life := activeLifetime.value
	activeLifetime.RUnlock()
	if life == nil {
		return false
	}
	life.stoppedCause(cause, detail)
	return true
}

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
	l.previousExit = "unrecorded"
	if data, err := readMarker(l.markerPath); err == nil {
		var previous runMarker
		if json.Unmarshal(data, &previous) == nil && previous.PID > 0 && !(previous.PID == l.pid && previous.Created == l.created) && !processRunning(previous.PID, previous.Created) {
			if validLifecycleCause(previous.ExitCause) {
				l.previousExit = previous.ExitCause
			} else {
				l.unclean = true
				l.append(fmt.Sprintf("Agent_b PID %d stopped: cause=killed; previous run started %s and ended without recording a reason", previous.PID, printable(previous.Started)))
			}
		}
	}
	l.started = l.now()
	marker, _ := json.Marshal(runMarker{PID: l.pid, Created: l.created, Started: l.stamp(), Application: l.applicationRoot, Listen: l.listen})
	temporary := l.markerPath + ".tmp"
	if os.WriteFile(temporary, marker, 0o600) == nil {
		_ = os.Rename(temporary, l.markerPath)
	}
}

// stopped records the first observed reason and clears the marker; later
// calls (a session end followed by the signal it causes) are ignored.
func (l *lifetime) stopped(reason string) {
	l.stoppedCause(classifyLifecycleCause(reason), reason)
}

var lifecycleCauses = map[string]bool{"user": true, "installer": true, "session_end": true, "crash": true, "killed": true, "unknown": true}

func validLifecycleCause(cause string) bool { return lifecycleCauses[cause] }

func classifyLifecycleCause(reason string) string {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "installer"), strings.Contains(lower, "asked to close"):
		return "installer"
	case strings.Contains(lower, "session"), strings.Contains(lower, "shutting down"), strings.Contains(lower, "logging off"):
		return "session_end"
	case strings.Contains(lower, "crash"), strings.Contains(lower, "panic"), strings.Contains(lower, "server failed"):
		return "crash"
	case strings.Contains(lower, "interrupt"), strings.Contains(lower, "operator"), strings.Contains(lower, "user"):
		return "user"
	case strings.Contains(lower, "signal"), strings.Contains(lower, "killed"):
		return "killed"
	default:
		return "unknown"
	}
}

func firstLine(value string) string {
	value = strings.TrimSpace(strings.Split(strings.ReplaceAll(value, "\r", ""), "\n")[0])
	return printable(value)
}

func (l *lifetime) stoppedCause(cause, detail string) {
	if !validLifecycleCause(cause) {
		cause = "unknown"
	}
	l.once.Do(func() {
		uptime := int64(0)
		if !l.started.IsZero() {
			uptime = max(int64(0), int64(l.now().Sub(l.started).Seconds()))
		}
		detail = firstLine(detail)
		l.append(fmt.Sprintf("Agent_b PID %d stopped: cause=%s; %s", l.pid, cause, detail))
		marker, _ := json.Marshal(runMarker{PID: l.pid, Created: l.created, Started: l.started.Format("2006-01-02 15:04:05 -07:00"), Application: l.applicationRoot, Listen: l.listen, ExitCause: cause, UptimeS: uptime, Ended: l.stamp()})
		temporary := l.markerPath + ".tmp"
		if os.WriteFile(temporary, marker, 0o600) == nil {
			_ = os.Rename(temporary, l.markerPath)
		}
		if cause == "user" {
			_ = os.WriteFile(filepath.Join(filepath.Dir(l.markerPath), "autostart-disabled"), []byte("user\n"), 0o600)
		}
		if l.onExit != nil {
			l.onExit(cause, uptime)
		}
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
