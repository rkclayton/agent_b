// Package modelinstall downloads and installs a pinned llama.cpp runtime and
// GGUF model beneath Agent_b's operator data root.
package modelinstall

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"harness/internal/updater"
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
	ModelID            string
	Backend            string
	AvailableBytes     uint64
	RAMBytes, GPUBytes uint64
}

// Outcome is retained locally only. Intake admission precedes any future emitter.
type Outcome struct {
	Step       string `json:"step"`
	Outcome    string `json:"outcome"`
	Error      string `json:"error,omitempty"`
	ElapsedS   int64  `json:"elapsed_s"`
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
	Backend    string `json:"backend"`
	RAMBytes   int64  `json:"ram_bytes"`
	GPUBytes   int64  `json:"gpu_bytes"`
}

type Manager struct {
	mu               sync.RWMutex
	root             string
	startupDir       string
	client           *http.Client
	requireHTTPS     bool
	runtime          Runtime
	models           []Model
	state            State
	cancel           context.CancelFunc
	done             chan struct{}
	start            func(string, []string, string) (int, error)
	onReady          func(context.Context, State) error
	last             Outcome
	logMu            sync.Mutex
	port             func() (int, error)
	healthTimeout    time.Duration
	missing          func(string) bool
	probe            func(context.Context, string) error
	verify           func(string) bool
	install          func(string) (uint32, error)
	dependencyClient *http.Client
	pending          Request
	confirmation     string
	confirmUntil     time.Time
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
	return &Manager{root: root, startupDir: startupDir, client: client, requireHTTPS: requireHTTPS, runtime: runtime, models: append([]Model(nil), models...), start: startDetached, onReady: onReady, port: freePort, healthTimeout: 60 * time.Second, missing: missingDependency, probe: probeRuntime, verify: microsoftSignature, install: runDependency, dependencyClient: &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" || len(via) >= 10 {
			return errors.New("dependency redirect refused")
		}
		return nil
	}}}
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
	if m.state.Running || m.done != nil {
		m.mu.Unlock()
		return errors.New("a model install is already running")
	}
	model, ok := m.model(request.ModelID)
	assets, backendOK := m.runtime.Backend[request.Backend]
	if !ok || !backendOK || len(assets) == 0 {
		m.mu.Unlock()
		return m.Reject(request, coded("catalog", errors.New("model or backend is not in the pinned catalog")))
	}
	total := model.Artifact.Bytes
	for _, asset := range assets {
		total += asset.Bytes
	}
	if request.AvailableBytes == 0 {
		m.state = State{}
		m.mu.Unlock()
		code := "memory"
		if request.Backend != "cpu" {
			code = "gpu"
		}
		return m.Reject(request, coded(code, errors.New("available memory for the selected backend is unknown")))
	}
	m.confirmation = ""
	m.pending = request
	if m.missing(request.Backend) {
		m.state = State{Phase: "dependency_missing", Text: dependencyLines["dependency_missing"], Error: dependencyLines["dependency_missing"]}
		m.mu.Unlock()
		m.finish(request, "preflight", "failed", time.Now(), coded("dependency_missing", errors.New("dependency_missing")))
		return nil
	}
	runContext, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	done := make(chan struct{})
	m.done = done
	m.state = State{Running: true, Phase: "queued", Text: "Preparing verified downloads", Total: total, Model: model.Label}
	m.mu.Unlock()
	go func() {
		defer func() { m.mu.Lock(); close(done); m.done = nil; m.mu.Unlock() }()
		m.run(runContext, model, request, assets)
	}()
	return nil
}

// Cancel stops the active install at its next context-aware boundary. Downloads
// remove their .part file, and onReady is never reached after cancellation.
func (m *Manager) Cancel() State {
	m.mu.Lock()
	m.confirmation = ""
	if m.state.Running && m.cancel != nil {
		m.cancel()
		m.state.Running = false
		m.state.Phase = "cancelled"
		m.state.Text = "Cancelled"
		m.state.Error = ""
	}
	state := m.state
	m.mu.Unlock()
	return state
}
func (m *Manager) Close() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	done := m.done
	m.mu.Unlock()
	if done != nil {
		<-done
	}
}

const dependencyURL = "https://aka.ms/vs/17/release/vc_redist.x64.exe"
const dependencyFailedLine = "The Visual C++ runtime was not installed. Install it from https://aka.ms/vs/17/release/vc_redist.x64.exe, then press Retry."

var dependencyLines = map[string]string{
	"dependency_missing":  "The local model needs the Microsoft Visual C++ runtime, which is not on this PC.",
	"dependency_declined": dependencyFailedLine, "dependency_failed": dependencyFailedLine,
	"dependency_unverified": "The download could not be verified as Microsoft's and was not run.",
	"dependency_restart":    "The Visual C++ runtime is installed. Restart this PC, then press Retry.",
}

// One short-lived capability, returned only to the confirming desktop tab.
// It never appears in Snapshot, and cancel, retry and consumption revoke it.
func (m *Manager) ConfirmDependency() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Phase != "dependency_missing" || m.done != nil {
		return "", errors.New("dependency confirmation unavailable")
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	m.confirmation = hex.EncodeToString(token[:])
	m.confirmUntil = time.Now().Add(5 * time.Minute)
	return m.confirmation, nil
}

func (m *Manager) InstallDependency(ctx context.Context, confirmation string) error {
	m.mu.Lock()
	if confirmation == "" || confirmation != m.confirmation || time.Now().After(m.confirmUntil) || m.done != nil || m.state.Phase != "dependency_missing" {
		m.mu.Unlock()
		return errors.New("dependency confirmation required")
	}
	m.confirmation = ""
	request := m.pending
	model, _ := m.model(request.ModelID)
	assets := m.runtime.Backend[request.Backend]
	ctx, m.cancel = context.WithCancel(ctx)
	done := make(chan struct{})
	m.done = done
	m.state = State{Running: true, Phase: "dependency_install", Text: "Installing the Visual C++ runtime…"}
	m.mu.Unlock()
	go func() {
		defer func() { m.mu.Lock(); close(done); m.done = nil; m.mu.Unlock() }()
		started := time.Now()
		m.logStep("dependency_install", "started", started, nil)
		code := m.installDependency(ctx)
		if code == "" && m.missing(request.Backend) {
			code = "dependency_failed"
		}
		if code == "" && ctx.Err() != nil {
			code = "dependency_declined"
		}
		if code != "" {
			m.finish(request, "dependency_install", "failed", started, coded(code, errors.New(code)))
			m.set(func(s *State) { *s = State{Phase: code, Text: dependencyLines[code], Error: dependencyLines[code]} })
			return
		}
		m.logStep("dependency_install", "completed", started, nil)
		total := model.Artifact.Bytes
		for _, asset := range assets {
			total += asset.Bytes
		}
		m.set(func(s *State) {
			*s = State{Running: true, Phase: "queued", Text: "Preparing verified downloads", Total: total, Model: model.Label}
		})
		m.run(ctx, model, request, assets)
	}()
	return nil
}

func microsoftSignature(path string) bool {
	s, err := updater.InspectSignature(path)
	return err == nil && s.Status == "Valid" && strings.HasPrefix(s.Subject, "CN=Microsoft Corporation,O=Microsoft Corporation,")
}

func (m *Manager) installDependency(ctx context.Context) (result string) {
	started := time.Now()
	step := "dependency_download"
	m.logStep(step, "started", started, nil)
	advance := func(next string) {
		m.logStep(step, "completed", started, nil)
		step = next
		m.logStep(step, "started", started, nil)
	}
	defer func() {
		if result != "" {
			m.logStep(step, "failed", started, coded(result, errors.New(result)))
		} else {
			m.logStep(step, "completed", started, nil)
		}
	}()
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return "dependency_failed"
	}
	dir, err := os.MkdirTemp(m.root, "dependency-")
	if err != nil {
		return "dependency_failed"
	}
	defer func() {
		if os.RemoveAll(dir) != nil {
			result = "dependency_failed"
		}
	}()
	path := filepath.Join(dir, "vc_redist.x64.exe")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dependencyURL, nil)
	if err != nil {
		return "dependency_failed"
	}
	res, err := m.dependencyClient.Do(req)
	if err != nil {
		return "dependency_failed"
	}
	defer res.Body.Close()
	const limit = 100 << 20
	if res.StatusCode != http.StatusOK || res.ContentLength > limit || res.Request.URL.Scheme != "https" {
		return "dependency_failed"
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "dependency_failed"
	}
	m.set(func(s *State) { s.Total = max(0, res.ContentLength); s.Downloaded = 0 })
	n, copyErr := io.Copy(io.MultiWriter(f, progressWriter{add: func(n int64) { m.set(func(s *State) { s.Downloaded += n }) }}), io.LimitReader(res.Body, limit+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || n == 0 || n > limit {
		return "dependency_failed"
	}
	unlock, err := lockDependencyFile(path)
	if err != nil {
		return "dependency_failed"
	}
	defer unlock()
	advance("dependency_verify")
	if !m.verify(path) {
		return "dependency_unverified"
	}
	if ctx.Err() != nil {
		return "dependency_declined"
	}
	advance("dependency_run")
	exit, err := m.install(path)
	if errors.Is(err, syscall.Errno(1223)) {
		return "dependency_declined"
	}
	if err != nil {
		return "dependency_failed"
	}
	if exit&0xffff0000 == 0x80070000 {
		exit &= 0xffff
	}
	switch exit {
	case 0, 1638:
		return ""
	case 1602, 1223:
		return "dependency_declined"
	case 3010, 1641:
		return "dependency_restart"
	default:
		return "dependency_failed"
	}
}

func (m *Manager) model(id string) (Model, bool) {
	for _, model := range m.models {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}

func (m *Manager) run(ctx context.Context, model Model, request Request, runtimeAssets []Artifact) {
	backend, availableBytes := request.Backend, request.AvailableBytes
	started := time.Now()
	step := "prepare"
	launchedPID := 0
	var launchedSpec Spec
	advance := func(next string) {
		m.logStep(step, "completed", started, nil)
		step = next
		m.logStep(step, "started", started, nil)
	}
	m.logStep(step, "started", started, nil)
	fail := func(err error) {
		if launchedPID > 0 {
			if process, claimErr := claimInstalled(launchedPID, launchedSpec); claimErr == nil {
				_ = process.stop()
			}
		}
		if errors.Is(err, context.Canceled) {
			m.finish(request, step, "canceled", started, nil)
			m.set(func(s *State) { s.Running = false; s.Phase = "cancelled"; s.Error = ""; s.Text = "Cancelled" })
			return
		}
		m.finish(request, step, "failed", started, err)
		line := FailureLine(step, failureCode(err))
		if failureCode(err) == "runtime_not_loadable" {
			line += " " + err.Error()
		}
		if strings.Contains(err.Error(), "HTTPS is required") {
			line = "Runtime download failed: HTTPS is required. Retry after updating the app."
		}
		m.set(func(s *State) { s.Running = false; s.Phase = "failed"; s.Error = line; s.Text = line })
	}
	installRoot := filepath.Join(m.root, "models")
	runtimeRoot := filepath.Join(installRoot, "llama.cpp", m.runtime.Version, backend)
	modelRoot := filepath.Join(installRoot, model.ID)
	if _, err := os.Stat(filepath.Join(installRoot, "start-model.cmd")); err == nil {
		generation, err := os.MkdirTemp(installRoot, "replacement-")
		if err != nil {
			fail(err)
			return
		}
		runtimeRoot = filepath.Join(generation, "runtime")
		modelRoot = filepath.Join(generation, "model")
	}
	var ownedFiles []string
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		fail(err)
		return
	}
	if err := os.MkdirAll(modelRoot, 0o700); err != nil {
		fail(err)
		return
	}
	for _, artifact := range runtimeAssets {
		advance("runtime_download")
		if err := ctx.Err(); err != nil {
			fail(err)
			return
		}
		archive := filepath.Join(runtimeRoot, artifact.Name)
		if err := m.download(ctx, "runtime", artifact, archive); err != nil {
			fail(err)
			return
		}
		advance("runtime_unpack")
		if err := extractZip(archive, runtimeRoot, 2<<30); err != nil {
			fail(coded("unpack", fmt.Errorf("extract verified runtime: %w", err)))
			return
		}
		ownedFiles = append(ownedFiles, archive)
		zipFile, err := zip.OpenReader(archive)
		if err != nil {
			fail(err)
			return
		}
		for _, entry := range zipFile.File {
			if !entry.FileInfo().IsDir() {
				ownedFiles = append(ownedFiles, filepath.Join(runtimeRoot, filepath.FromSlash(entry.Name)))
			}
		}
		zipFile.Close()
	}
	advance("server_find")
	serverPath, err := findServer(runtimeRoot)
	if err != nil {
		fail(err)
		return
	}
	advance("runtime_check")
	if err := m.probe(ctx, serverPath); err != nil {
		fail(coded("runtime_not_loadable", err))
		return
	}
	modelPath := filepath.Join(modelRoot, model.Artifact.Name)
	advance("model_download")
	if err := ctx.Err(); err != nil {
		fail(err)
		return
	}
	if err := m.download(ctx, "model", model.Artifact, modelPath); err != nil {
		fail(err)
		return
	}
	advance("metadata")
	metadata, err := readGGUFMetadata(modelPath)
	if err != nil {
		fail(coded("metadata", fmt.Errorf("read model context metadata: %w", err)))
		return
	}
	advance("context")
	sizing, err := computeContext(model.Artifact.Bytes, availableBytes, metadata)
	if err != nil {
		fail(coded("memory", err))
		return
	}
	sizingPath := filepath.Join(modelRoot, "context-sizing.json")
	sizingJSON, _ := json.MarshalIndent(sizing, "", "  ")
	if err := os.WriteFile(sizingPath, append(sizingJSON, '\n'), 0o600); err != nil {
		fail(fmt.Errorf("write context sizing: %w", err))
		return
	}
	advance("port")
	port, err := m.port()
	if err != nil {
		fail(err)
		return
	}
	args := []string{"-m", modelPath, "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--ctx-size", fmt.Sprint(sizing.NCtx)}
	advance("server_start")
	pid, err := m.start(serverPath, args, installRoot)
	if err != nil {
		fail(coded("server_start", fmt.Errorf("start verified llama-server: %w", err)))
		return
	}
	launchedPID, launchedSpec = pid, Spec{Root: installRoot, Executable: serverPath}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	m.set(func(s *State) {
		s.Phase = "starting"
		s.Text = fmt.Sprintf("Starting llama-server (PID %d)", pid)
		s.BaseURL = baseURL
		s.Context = &sizing
	})
	advance("health")
	if err := waitHealth(ctx, m.client, baseURL+"/health", m.healthTimeout); err != nil {
		if p, claimErr := claimInstalled(pid, Spec{Root: installRoot, Executable: serverPath}); claimErr == nil {
			_ = p.stop()
		}
		fail(fmt.Errorf("llama-server did not become ready: %w", err))
		return
	}
	if err := ctx.Err(); err != nil {
		fail(err)
		return
	}
	// llama-server reports the loaded GGUF path as its model id. Persist that
	// exact id so the connection created by the wizard agrees with /v1/models.
	ready := State{Running: false, Phase: "ready", Text: "Model installed and ready", Total: m.Snapshot().Total, Downloaded: m.Snapshot().Downloaded, ConnectionID: "installed-local", BaseURL: baseURL, Model: modelPath, Context: &sizing}
	ownedFiles = append(ownedFiles, modelPath, sizingPath)
	var installedBytes int64
	for _, path := range ownedFiles {
		if info, err := os.Stat(path); err == nil {
			installedBytes += info.Size()
		}
	}
	advance("startup")
	if _, err := writeAutostart("", installRoot, serverPath, args); err != nil {
		fail(err)
		return
	}
	advance("manifest")
	if err := SaveSpec(Spec{ID: ready.ConnectionID, Root: installRoot, Executable: serverPath, URL: baseURL, Label: model.Label, Args: args, Files: ownedFiles, Bytes: installedBytes, IdleMinutes: 60}); err != nil {
		fail(err)
		return
	}
	advance("connection")
	if m.onReady != nil {
		if err := m.onReady(ctx, ready); err != nil {
			if p, claimErr := claimInstalled(pid, Spec{Root: installRoot, Executable: serverPath}); claimErr == nil {
				_ = p.stop()
			}
			fail(fmt.Errorf("create local connection: %w", err))
			return
		}
	}
	advance("done")
	m.finish(request, step, "ok", started, nil)
	m.mu.Lock()
	m.state = ready
	m.cancel = nil
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
		return coded("http_status", fmt.Errorf("download %s: HTTP %d", artifact.Name, response.StatusCode))
	}
	if response.ContentLength >= 0 && response.ContentLength != artifact.Bytes {
		return coded("size", fmt.Errorf("download %s: size %d, expected %d", artifact.Name, response.ContentLength, artifact.Bytes))
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
		return coded("size", fmt.Errorf("download %s: wrote %d bytes, expected %d", artifact.Name, written, artifact.Bytes))
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, artifact.SHA256) {
		return coded("hash_mismatch", fmt.Errorf("download %s: SHA-256 %s, expected %s", artifact.Name, actual, artifact.SHA256))
	}
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	keepTemporary = true
	return nil
}

type progressWriter struct{ add func(int64) }

type installFailure struct {
	code string
	err  error
}

func (e installFailure) Error() string   { return e.err.Error() }
func (e installFailure) Unwrap() error   { return e.err }
func coded(code string, err error) error { return installFailure{code, err} }
func failureCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.Errno(112)) {
		return "disk_space"
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return "port_in_use"
	}
	if errors.Is(err, os.ErrPermission) {
		return "permission"
	}
	var explicit installFailure
	if errors.As(err, &explicit) {
		return explicit.code
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return "timeout"
		}
	}
	var operation *net.OpError
	var request *url.Error
	if errors.As(err, &operation) || errors.As(err, &request) {
		return "network"
	}
	return "other"
}

var installSteps = map[string]string{"preflight": "Install checks", "prepare": "Prepare folders", "runtime_download": "Runtime download", "runtime_unpack": "Runtime unpack", "model_download": "Model download", "metadata": "Model metadata", "context": "Model memory", "server_find": "Find server", "port": "Reserve port", "server_start": "Start server", "health": "Server readiness", "startup": "Save launcher", "manifest": "Save model record", "connection": "Save connection", "done": "Install"}
var installActions = map[string]string{"network": "Check the connection, then Retry with Install.", "http_status": "Retry with Install later.", "disk_space": "Free disk space, then Retry with Install.", "hash_mismatch": "Retry with Install to download again.", "unpack": "Retry with Install to download again.", "size": "Retry with Install to download again.", "metadata": "Retry with Install to download again.", "permission": "Check folder access, then Retry with Install.", "gpu": "Check the GPU driver, then Retry with Install.", "memory": "Free memory, then Retry with Install.", "catalog": "Update the app, then Retry with Install.", "port_in_use": "Free the local port, then Retry with Install."}

func FailureLine(step, code string) string {
	label := installSteps[step]
	if step == "runtime_check" {
		label = "Runtime"
	}
	if label == "" {
		label = "Install"
	}
	action := installActions[code]
	if action == "" {
		action = "Check model-install.log, then Retry with Install."
	}
	return label + " failed (" + strings.ReplaceAll(code, "_", " ") + "). " + action
}
func sizeBucket(n uint64) int64 {
	if n == 0 {
		return 0
	}
	if n >= 1<<53 {
		return 1 << 53
	}
	return 1 << bits.Len64(n-1)
}
func (m *Manager) LastOutcome() Outcome { m.mu.RLock(); defer m.mu.RUnlock(); return m.last }
func (m *Manager) Reject(request Request, err error) error {
	m.finish(request, "preflight", "failed", time.Now(), err)
	return errors.New(FailureLine("preflight", failureCode(err)))
}

func (m *Manager) RejectMemory(request Request, err error) error {
	code := "memory"
	if request.Backend != "cpu" {
		code = "gpu"
	}
	return m.Reject(request, coded(code, err))
}
func (m *Manager) finish(request Request, step, outcome string, started time.Time, err error) {
	state := m.Snapshot()
	backend := request.Backend
	if backend != "cpu" && backend != "cuda" && backend != "vulkan" {
		backend = "unknown"
	}
	if step == "preflight" {
		state.Downloaded, state.Total = 0, 0
	}
	ram := request.RAMBytes
	if ram == 0 && backend == "cpu" {
		ram = request.AvailableBytes
	}
	value := Outcome{Step: step, Outcome: outcome, ElapsedS: min(2147483647, int64(time.Since(started).Seconds())), BytesDone: sizeBucket(uint64(max(0, state.Downloaded))), BytesTotal: sizeBucket(uint64(max(0, state.Total))), Backend: backend, RAMBytes: sizeBucket(ram), GPUBytes: sizeBucket(request.GPUBytes)}
	if err != nil {
		value.Error = failureCode(err)
	}
	m.mu.Lock()
	m.last = value
	m.mu.Unlock()
	m.logStep(step, outcome, started, err)
}

const installLogSlots, installLogSlotBytes, installLogHeader = 256, 2048, 64

var logURL = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
var logSecret = regexp.MustCompile(`(?i)(authorization|bearer|api[_-]?key|password|secret|token)[=: \t]+[^\s,;]+`)
var logAddress = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?|\[[0-9a-fA-F:]+\](?::\d+)?`)

func localInstallError(err error) string {
	if err == nil {
		return ""
	}
	// URL credentials, query strings, address literals and keyed secrets never persist.
	text := logURL.ReplaceAllStringFunc(err.Error(), func(raw string) string {
		u, e := url.Parse(raw)
		if e != nil {
			return "[download]"
		}
		if net.ParseIP(u.Hostname()) != nil {
			return "[download host]"
		}
		return u.Hostname()
	})
	text = logAddress.ReplaceAllString(text, "[address]")
	text = logSecret.ReplaceAllString(text, "[redacted]")
	text = strings.ToValidUTF8(text, "")
	if len(text) > 512 {
		text = strings.ToValidUTF8(text[:509], "") + "…"
	}
	return text
}

// A fixed-slot append log: each step overwrites at most one oldest slot and its
// sequence header. It never copies or rewrites the retained collection.
func (m *Manager) logStep(step, status string, started time.Time, err error) {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	dir := filepath.Join(m.root, "logs")
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	f, e := os.OpenFile(filepath.Join(dir, "model-install.log"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil {
		return
	} else if info.Size() > installLogHeader+installLogSlots*installLogSlotBytes {
		if f.Truncate(installLogHeader+installLogSlots*installLogSlotBytes) != nil {
			return
		}
	}
	header := make([]byte, installLogHeader)
	_, _ = f.ReadAt(header, 0)
	seq, _ := strconv.ParseUint(strings.TrimSpace(string(header)), 10, 64)
	seq++
	state := m.Snapshot()
	record := map[string]any{"seq": seq, "at": time.Now().UTC().Format(time.RFC3339), "step": step, "status": status, "elapsed_ms": time.Since(started).Milliseconds(), "bytes_done": state.Downloaded, "bytes_total": state.Total}
	if err != nil {
		record["error"] = localInstallError(err)
		record["class"] = fmt.Sprintf("%T", err)
		record["reason"] = failureCode(err)
	}
	line, e := json.Marshal(record)
	if e != nil || len(line) >= installLogSlotBytes {
		return
	}
	slot := []byte(strings.Repeat(" ", installLogSlotBytes))
	copy(slot, line)
	slot[len(slot)-1] = '\n'
	if _, e = f.WriteAt(slot, installLogHeader+int64((seq-1)%installLogSlots)*installLogSlotBytes); e != nil {
		return
	}
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%063d\n", seq)), 0)
}

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
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
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
	return context.DeadlineExceeded
}

func writeAutostart(startupDir, installRoot, server string, args []string) (string, error) {
	command := filepath.Join(installRoot, "start-model.cmd")
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, `"`+strings.ReplaceAll(arg, `"`, `""`)+`"`)
	}
	if err := os.WriteFile(command, []byte("@echo off\r\n\""+server+"\" "+strings.Join(quoted, " ")+"\r\n"), 0o600); err != nil {
		return "", err
	}
	return command, nil
}
