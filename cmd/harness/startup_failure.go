package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Item 2x0: a window launch that cannot start says why on screen. The operator
// upgraded a second machine, the new build refused its configuration, and every
// launch after that ended in nothing visible: the reason was only in a log file.

// startupFailureBound is how long a window launch waits for its own listener
// before it shows why it has not answered. It equals the launcher's own wait
// (launch-Agent_b.ps1 -StartupTimeoutSeconds 120), which was set above the
// 45-74 s cold starts measured on the operator's machine.
const startupFailureBound = 120 * time.Second

// startupLogTail holds this process's last log lines for the failure window,
// because a launch from the Start menu has no console and no startup log file.
var startupLogTail = &recentLines{limit: 40}

// startupFailure is what the failure window needs; it is filled as start-up learns it.
var startupFailure struct {
	sync.Mutex
	window   bool
	dataRoot string
	shown    bool
}

type recentLines struct {
	sync.Mutex
	limit int
	lines []string
}

func (r *recentLines) Write(p []byte) (int, error) {
	r.Lock()
	defer r.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\r\n"), "\n") {
		if len(r.lines) == r.limit {
			r.lines = r.lines[1:]
		}
		r.lines = append(r.lines, strings.TrimRight(line, "\r"))
	}
	return len(p), nil
}

func (r *recentLines) String() string {
	r.Lock()
	defer r.Unlock()
	return strings.Join(r.lines, "\r\n")
}

// startupFatal replaces log.Fatal on the start-up path: it logs, shows the failure
// window when this launch asked for a window, and exits 1.
func startupFatal(v ...any) {
	reason := fmt.Sprint(v...)
	log.Print(reason)
	showStartupFailureOnce(reason)
	os.Exit(1)
}

func startupFatalf(format string, v ...any) { startupFatal(fmt.Sprintf(format, v...)) }

// watchStartup shows the failure window if the listener has not opened within the bound.
func watchStartup() {
	time.AfterFunc(startupFailureBound, func() {
		if !startupTimer.listened() {
			showStartupFailureOnce(fmt.Sprintf("Agent_b did not answer within %d seconds.", int(startupFailureBound.Seconds())))
		}
	})
}

func showStartupFailureOnce(reason string) {
	startupFailure.Lock()
	if !startupFailure.window || startupFailure.shown || os.Getenv("AGENTB_NO_HOST_WINDOW") != "" {
		startupFailure.Unlock()
		return
	}
	startupFailure.shown = true
	dataRoot := startupFailure.dataRoot
	startupFailure.Unlock()
	if dataRoot != "" {
		appendLauncherMessage(dataRoot, "Agent_b startup error: "+reason)
	}
	showStartupFailure(startupFailureReport(reason, dataRoot, startupLogTail.String()))
}

// startupFailureReport is the failure window's text: the reason, then the last
// lines of launcher-errors.log, then this process's own last log lines.
func startupFailureReport(reason, dataRoot, ownLog string) string {
	var report strings.Builder
	report.WriteString("Agent_b could not start.\r\n\r\n" + reason + "\r\n")
	if dataRoot != "" {
		path := filepath.Join(dataRoot, "logs", "launcher-errors.log")
		report.WriteString("\r\n" + path + ":\r\n" + strings.Join(lastLines(path, 10), "\r\n") + "\r\n")
	}
	if ownLog != "" {
		report.WriteString("\r\nAgent_b log:\r\n" + ownLog + "\r\n")
	}
	return report.String()
}

// lastLines reads at most the last 64 KiB of an append-only log.
func lastLines(path string, count int) []string {
	file, err := os.Open(path)
	if err != nil {
		return []string{"(" + err.Error() + ")"}
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr == nil && info.Size() > 64<<10 {
		_, _ = file.Seek(info.Size()-64<<10, 0)
	}
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return lines
}
