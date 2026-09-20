package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
func (s *Server) speechStreamHandler(w http.ResponseWriter, r *http.Request) {
	status := s.speechStatus(r.Context())
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming is unavailable"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	if !status.Available {
		payload, _ := json.Marshal(map[string]any{"error": status.Reason, "done": true})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		return
	}
	// A helper that can drive the recogniser is not built yet; saying so here
	// is better than holding the stream open and letting the operator believe
	// the machine is listening.
	payload, _ := json.Marshal(map[string]any{"error": "the dictation helper is not installed", "done": true})
	fmt.Fprintf(w, "data: %s\n\n", payload)
	flusher.Flush()
}

// speechStop ends a dictation. It is safe to call when nothing is listening,
// which is what the composer does when a send interrupts one.
func (s *Server) speechStopHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": true})
}
