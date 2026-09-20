package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Item 2ge (v1.2.2/W1): what the microphone can do on this host, answered by
// Windows rather than assumed.
//
// The operator asked for Windows' own recogniser, the one behind Win+H, run
// without its window. So the harness asks the host what it has and the mic's
// hover text says exactly that. There is no browser speech API and no audio
// leaves the machine: when a recogniser can be driven, it is driven locally by
// a helper the harness spawns; when it cannot, the control is disabled and
// says why rather than pretending.

// SpeechStatus is what the composer's microphone reads.
type SpeechStatus struct {
	// Available is whether dictation can actually run here. False disables the
	// control, and Reason is what its hover text says.
	Available bool `json:"available"`
	// Offline is whether the local speech pack answers, so the operator knows
	// whether anything would go to Microsoft's service if it did run.
	Offline bool   `json:"offline"`
	Reason  string `json:"reason"`
	// Languages are the grammar languages the host reports.
	Languages []string `json:"languages,omitempty"`
}

type speechProbe struct {
	mu      sync.Mutex
	status  *SpeechStatus
	checked time.Time
	// The helper that is listening right now, and the one way to end it.
	listening *exec.Cmd
	stop      context.CancelFunc
}

// speechStatus probes once and caches: the answer changes only when Windows
// gains or loses a speech pack, and the probe runs PowerShell.
func (s *Server) speechStatus(ctx context.Context) SpeechStatus {
	s.speech.mu.Lock()
	defer s.speech.mu.Unlock()
	if s.speech.status != nil && time.Since(s.speech.checked) < time.Hour {
		return *s.speech.status
	}
	status := s.probeSpeech(ctx)
	s.speech.status = &status
	s.speech.checked = time.Now()
	return status
}

func (s *Server) probeSpeech(ctx context.Context) SpeechStatus {
	script := filepath.Join(s.roots.Application, "scripts", "speech-probe.ps1")
	// Item 2gc: Windows' own utilities by absolute path, never a bare name.
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	probeContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeContext, powershell, "-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script).Output()
	if err != nil {
		return SpeechStatus{Reason: fmt.Sprintf("the host could not be asked about dictation: %v", err)}
	}
	var status SpeechStatus
	line := strings.TrimSpace(string(output))
	if index := strings.LastIndex(line, "{"); index > 0 {
		line = line[index:]
	}
	if err := json.Unmarshal([]byte(line), &status); err != nil {
		return SpeechStatus{Reason: "the host's answer about dictation could not be read"}
	}
	return status
}

// speech answers GET /api/speech with the host finding.
func (s *Server) speechHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	writeJSON(w, http.StatusOK, s.speechStatus(r.Context()))
}

// speechStream is the partial-results stream the composer reads while the
// operator dictates. Where dictation cannot run, it says so once and closes,
// so the page never waits on a stream that will never speak.
//
// Item 2ge (v1.2.4): it drives scripts/speech-helper.ps1, which runs the
// recogniser Windows ships IN THIS MACHINE and writes one JSON line per
// hypothesis and per utterance. Those lines are forwarded as they arrive. No
// audio and no text made from it goes anywhere else: the helper reads an audio
// buffer and writes to a pipe, and this handler writes that pipe to the page
// that asked for it.
func (s *Server) speechStreamHandler(w http.ResponseWriter, r *http.Request) {
	status := s.speechStatus(r.Context())
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming is unavailable"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	send := func(value any) {
		payload, _ := json.Marshal(value)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}
	if !status.Available {
		send(map[string]any{"error": status.Reason, "done": true})
		return
	}

	script := filepath.Join(s.roots.Application, "scripts", "speech-helper.ps1")
	if _, err := os.Stat(script); err != nil {
		send(map[string]any{"error": "the dictation helper is not installed", "done": true})
		return
	}
	// Item 2gc: Windows' own utilities by absolute path, never a bare name.
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	arguments := []string{"-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	// A wave file is the gate's input, and it is accepted ONLY from the
	// application's own fixtures: the page may ask to be proved, not to have an
	// arbitrary file read. Anything else is the microphone.
	if name := r.URL.Query().Get("fixture"); name != "" {
		if !fixtureName.MatchString(name) {
			send(map[string]any{"error": "that is not a fixture name", "done": true})
			return
		}
		arguments = append(arguments, "-WaveFile", filepath.Join(s.roots.Application, "tests", "fixtures", "audio", name))
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	command := exec.CommandContext(ctx, powershell, arguments...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		send(map[string]any{"error": "the dictation helper could not be started", "done": true})
		return
	}
	if err := command.Start(); err != nil {
		send(map[string]any{"error": fmt.Sprintf("the dictation helper could not be started: %v", err), "done": true})
		return
	}
	s.speech.mu.Lock()
	s.speech.listening = command
	s.speech.stop = cancel
	s.speech.mu.Unlock()
	defer func() {
		s.speech.mu.Lock()
		if s.speech.listening == command {
			s.speech.listening = nil
			s.speech.stop = nil
		}
		s.speech.mu.Unlock()
		cancel()
		_ = command.Wait()
	}()

	reader := bufio.NewScanner(stdout)
	reader.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for reader.Scan() {
		line := strings.TrimSpace(reader.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			continue
		}
		send(value)
		if done, _ := value["done"].(bool); done {
			return
		}
	}
	send(map[string]any{"done": true, "reason": "ended"})
}

// fixtureName is deliberately narrow: a plain file name inside the application
// fixtures, never a path.
var fixtureName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}\.wav$`)

// speechStop ends a dictation. It is safe to call when nothing is listening,
// which is what the composer does when a send interrupts one.
func (s *Server) speechStopHandler(w http.ResponseWriter, r *http.Request) {
	s.speech.mu.Lock()
	stop := s.speech.stop
	s.speech.mu.Unlock()
	// Hard stop (12): the helper is ended by cancelling the context that owns
	// its process, which signals that PID and nothing else.
	if stop != nil {
		stop()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": true})
}
