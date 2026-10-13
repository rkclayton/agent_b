package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"harness/internal/quietproc"
)

var shippedChatFaces = []string{"IBM Plex Sans", "Atkinson Hyperlegible", "OpenDyslexic"}

type fontState struct {
	mu     sync.Mutex
	fonts  []string
	loader func() ([]string, error)
}

func installedFontFamilies() ([]string, error) {
	if runtime.GOOS != "windows" {
		return nil, nil
	}
	script := `[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); Add-Type -AssemblyName System.Drawing; [System.Drawing.Text.InstalledFontCollection]::new().Families.Name | Sort-Object -Unique | ConvertTo-Json -Compress`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := quietproc.Quiet(exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script))
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	var families []string
	if err := json.Unmarshal(output, &families); err != nil {
		var one string
		if oneErr := json.Unmarshal(output, &one); oneErr != nil {
			return nil, err
		}
		families = []string{one}
	}
	return families, nil
}

func (s *Server) fontFamilies(refresh bool) ([]string, error) {
	s.fontState.mu.Lock()
	defer s.fontState.mu.Unlock()
	if len(s.fontState.fonts) > 0 && !refresh {
		return append([]string(nil), s.fontState.fonts...), nil
	}
	loader := s.fontState.loader
	if loader == nil {
		loader = installedFontFamilies
	}
	installed, err := loader()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	result := append([]string(nil), shippedChatFaces...)
	for _, family := range result {
		seen[strings.ToLower(family)] = true
	}
	cleaned := installed[:0]
	for _, family := range installed {
		if family = strings.TrimSpace(family); family != "" {
			cleaned = append(cleaned, family)
		}
	}
	sort.Slice(cleaned, func(i, j int) bool {
		left, right := strings.ToLower(cleaned[i]), strings.ToLower(cleaned[j])
		if left == right {
			return cleaned[i] < cleaned[j]
		}
		return left < right
	})
	for _, family := range cleaned {
		if family != "" && !seen[strings.ToLower(family)] {
			seen[strings.ToLower(family)] = true
			result = append(result, family)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("no font families found")
	}
	s.fontState.fonts = result
	return append([]string(nil), result...), nil
}

func (s *Server) fontsEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	families, err := s.fontFamilies(r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "installed fonts could not be read", "fonts")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"families": families})
}
