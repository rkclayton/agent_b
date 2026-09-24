// Package modelinstall downloads and installs a pinned llama.cpp runtime and
// GGUF model beneath Agent_b's operator data root.
package modelinstall

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Name   string `json:"name"`
}

type Model struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	MinGiB   int      `json:"min_gib"`
	MaxGiB   int      `json:"max_gib,omitempty"`
	Source   string   `json:"source"`
	Artifact Artifact `json:"artifact"`
}

type Runtime struct {
	Version string                `json:"version"`
	Source  string                `json:"source"`
	Backend map[string][]Artifact `json:"backend"`
}

type State struct {
	Running      bool           `json:"running"`
	Phase        string         `json:"phase"`
	Text         string         `json:"text"`
	Downloaded   int64          `json:"downloaded_bytes"`
	Total        int64          `json:"total_bytes"`
	Error        string         `json:"error,omitempty"`
	ConnectionID string         `json:"connection_id,omitempty"`
	BaseURL      string         `json:"base_url,omitempty"`
	Model        string         `json:"model,omitempty"`
	Context      *ContextSizing `json:"context,omitempty"`
}

type Request struct {
	ModelID        string
	Backend        string
	AvailableBytes uint64
}

type Manager struct {
	mu           sync.RWMutex
	root         string
	startupDir   string
	client       *http.Client
	requireHTTPS bool
	runtime      Runtime
	models       []Model
	state        State
	start        func(string, []string, string) (int, error)
	onReady      func(context.Context, State) error
}

func New(root, startupDir string, client *http.Client, runtime Runtime, models []Model, onReady func(context.Context, State) error) *Manager {
	requireHTTPS := client == nil
	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Minute,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if request.URL.Scheme != "https" {
					return errors.New("model download redirect refused: HTTPS is required")
				}
				if len(via) >= 10 {
					return errors.New("model download redirect refused: too many redirects")
				}
				return nil
			},
		}
	}
	return &Manager{root: root, startupDir: startupDir, client: client, requireHTTPS: requireHTTPS, runtime: runtime, models: append([]Model(nil), models...), start: startDetached, onReady: onReady}
}

func (m *Manager) Catalog(memoryBytes uint64, cuda, vulkan bool) map[string]any {
	available := int(memoryBytes / (1024 * 1024 * 1024))
	models := make([]Model, 0, len(m.models))
	for _, model := range m.models {
		if memoryBytes == 0 || available >= model.MinGiB {
			models = append(models, model)
		}
	}
	backends := []string{"cpu"}
	if vulkan {
		backends = append(backends, "vulkan")
	}
	if cuda {
		backends = append(backends, "cuda")
	}
	return map[string]any{"runtime_version": m.runtime.Version, "runtime_source": m.runtime.Source, "models": models, "backends": backends}
}

func (m *Manager) Snapshot() State         { m.mu.RLock(); defer m.mu.RUnlock(); return m.state }
func (m *Manager) set(update func(*State)) { m.mu.Lock(); update(&m.state); m.mu.Unlock() }

func (m *Manager) Start(ctx context.Context, request Request) error {
	m.mu.Lock()
	if m.state.Running {
		m.mu.Unlock()
		return errors.New("a model install is already running")
	}
	model, ok := m.model(request.ModelID)
	assets, backendOK := m.runtime.Backend[request.Backend]
	if !ok || !backendOK || len(assets) == 0 {
		m.mu.Unlock()
		return errors.New("model or backend is not in the pinned catalog")
	}
	total := model.Artifact.Bytes
	for _, asset := range assets {
		total += asset.Bytes
	}
	if request.AvailableBytes == 0 {
		m.state = State{}
		m.mu.Unlock()
		return errors.New("available memory for the selected backend is unknown")
	}
	m.state = State{Running: true, Phase: "queued", Text: "Preparing verified downloads", Total: total, Model: model.Label}
	m.mu.Unlock()
	go m.run(ctx, model, request.Backend, request.AvailableBytes, assets)
	return nil
}

func (m *Manager) model(id string) (Model, bool) {
	for _, model := range m.models {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}

func (m *Manager) run(ctx context.Context, model Model, backend string, availableBytes uint64, runtimeAssets []Artifact) {
	fail := func(err error) {
		m.set(func(s *State) { s.Running = false; s.Phase = "failed"; s.Error = err.Error(); s.Text = err.Error() })
	}
	installRoot := filepath.Join(m.root, "models")
	runtimeRoot := filepath.Join(installRoot, "llama.cpp", m.runtime.Version, backend)
	modelRoot := filepath.Join(installRoot, model.ID)
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		fail(err)
		return
	}
	if err := os.MkdirAll(modelRoot, 0o700); err != nil {
		fail(err)
		return
	}
	for _, artifact := range runtimeAssets {
		archive := filepath.Join(runtimeRoot, artifact.Name)
		if err := m.download(ctx, "runtime", artifact, archive); err != nil {
			fail(err)
			return
		}
		if err := extractZip(archive, runtimeRoot, 2<<30); err != nil {
			fail(fmt.Errorf("extract verified runtime: %w", err))
			return
		}
	}
	modelPath := filepath.Join(modelRoot, model.Artifact.Name)
	if err := m.download(ctx, "model", model.Artifact, modelPath); err != nil {
		fail(err)
		return
	}
	metadata, err := readGGUFMetadata(modelPath)
	if err != nil {
		fail(fmt.Errorf("read model context metadata: %w", err))
		return
	}
	sizing, err := computeContext(model.Artifact.Bytes, availableBytes, metadata)
	if err != nil {
		fail(err)
		return
	}
	sizingPath := filepath.Join(modelRoot, "context-sizing.json")
	sizingJSON, _ := json.MarshalIndent(sizing, "", "  ")
	if err := os.WriteFile(sizingPath, append(sizingJSON, '\n'), 0o600); err != nil {
		fail(fmt.Errorf("write context sizing: %w", err))
		return
	}
	serverPath, err := findServer(runtimeRoot)
	if err != nil {
		fail(err)
		return
	}
	port, err := freePort()
	if err != nil {
		fail(err)
		return
	}
	args := []string{"-m", modelPath, "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--ctx-size", fmt.Sprint(sizing.NCtx)}
	launcher, err := writeAutostart(m.startupDir, installRoot, serverPath, args)
	if err != nil {
		fail(err)
		return
	}
	pid, err := m.start(serverPath, args, installRoot)
	if err != nil {
		fail(fmt.Errorf("start verified llama-server: %w", err))
		return
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	m.set(func(s *State) {
		s.Phase = "starting"
		s.Text = fmt.Sprintf("Starting llama-server (PID %d)", pid)
		s.BaseURL = baseURL
		s.Context = &sizing
	})
	if err := waitHealth(ctx, m.client, baseURL+"/health", 60*time.Second); err != nil {
		fail(fmt.Errorf("llama-server did not become ready; autostart retained at %s: %w", launcher, err))
		return
	}
	// llama-server reports the loaded GGUF path as its model id. Persist that
	// exact id so the connection created by the wizard agrees with /v1/models.
	ready := State{Running: false, Phase: "ready", Text: "Model installed and ready", Total: m.Snapshot().Total, Downloaded: m.Snapshot().Downloaded, ConnectionID: "installed-local", BaseURL: baseURL, Model: modelPath, Context: &sizing}
	if m.onReady != nil {
		if err := m.onReady(ctx, ready); err != nil {
			fail(fmt.Errorf("create local connection: %w", err))
			return
		}
	}
	m.mu.Lock()
	m.state = ready
	m.mu.Unlock()
}

func (m *Manager) download(ctx context.Context, phase string, artifact Artifact, target string) error {
	if len(artifact.SHA256) != 64 || artifact.Bytes <= 0 || artifact.URL == "" {
		return errors.New("catalog artifact is missing its pinned identity")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return err
	}
	if m.requireHTTPS && request.URL.Scheme != "https" {
		return fmt.Errorf("download %s: HTTPS is required", artifact.Name)
	}
	response, err := m.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", artifact.Name, response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != artifact.Bytes {
		return fmt.Errorf("download %s: size %d, expected %d", artifact.Name, response.ContentLength, artifact.Bytes)
	}
	temporary := target + ".part"
	_ = os.Remove(temporary)
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.Remove(temporary)
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash, progressWriter{add: func(n int64) {
		m.set(func(s *State) { s.Phase = phase; s.Text = "Downloading " + artifact.Name; s.Downloaded += n })
	}}), io.LimitReader(response.Body, artifact.Bytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != artifact.Bytes {
		return fmt.Errorf("download %s: wrote %d bytes, expected %d", artifact.Name, written, artifact.Bytes)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, artifact.SHA256) {
		return fmt.Errorf("download %s: SHA-256 %s, expected %s", artifact.Name, actual, artifact.SHA256)
	}
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	keepTemporary = true
	return nil
}

type progressWriter struct{ add func(int64) }

func (w progressWriter) Write(p []byte) (int, error) { w.add(int64(len(p))); return len(p), nil }

func extractZip(archive, destination string, limit int64) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()
	root, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	var total int64
	for _, entry := range reader.File {
		total += int64(entry.UncompressedSize64)
		if total > limit {
			return errors.New("runtime archive exceeds extraction limit")
		}
		target := filepath.Join(root, filepath.FromSlash(entry.Name))
		absolute, err := filepath.Abs(target)
		if err != nil || (absolute != root && !strings.HasPrefix(absolute, root+string(os.PathSeparator))) {
			return errors.New("runtime archive contains an unsafe path")
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(absolute, 0o700); err != nil {
				return err
			}
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return errors.New("runtime archive contains a link")
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
			return err
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		targetFile, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
		if err != nil {
			source.Close()
			return err
		}
		written, copyErr := io.Copy(targetFile, io.LimitReader(source, int64(entry.UncompressedSize64)+1))
		closeTarget, closeSource := targetFile.Close(), source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeTarget != nil {
			return closeTarget
		}
		if closeSource != nil {
			return closeSource
		}
		if written != int64(entry.UncompressedSize64) {
			return fmt.Errorf("runtime archive entry %s wrote %d bytes, expected %d", entry.Name, written, entry.UncompressedSize64)
		}
	}
	return nil
}

func findServer(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(entry.Name(), "llama-server.exe") {
			if found != "" {
				return errors.New("runtime contains more than one llama-server.exe")
			}
			found = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("verified runtime contains no llama-server.exe")
	}
	return found, nil
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func waitHealth(ctx context.Context, client *http.Client, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return errors.New("health timeout")
}

func writeAutostart(startupDir, installRoot, server string, args []string) (string, error) {
	if startupDir == "" {
		return "", errors.New("the sign-in Startup folder is unavailable")
	}
	if err := os.MkdirAll(startupDir, 0o700); err != nil {
		return "", err
	}
	command := filepath.Join(installRoot, "start-model.cmd")
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, `"`+strings.ReplaceAll(arg, `"`, `""`)+`"`)
	}
	if err := os.WriteFile(command, []byte("@echo off\r\n\""+server+"\" "+strings.Join(quoted, " ")+"\r\n"), 0o600); err != nil {
		return "", err
	}
	launcher := filepath.Join(startupDir, "Agent_b-model.vbs")
	vbs := `CreateObject("WScript.Shell").Run """` + strings.ReplaceAll(command, `"`, `""`) + `""", 0, False` + "\r\n"
	if err := os.WriteFile(launcher, []byte(vbs), 0o600); err != nil {
		return "", err
	}
	return launcher, nil
}
