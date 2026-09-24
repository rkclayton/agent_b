// Package updater performs Agent_b's single passive release check and the
// operator-triggered, hash-verified installer download.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const LatestReleaseURL = "https://api.github.com/repos/rkclayton/agent_b/releases/latest"

const (
	manifestName = "release.json"
	setupName    = "Agent_b-setup.exe"
	maxMetadata  = 1 << 20
)

var hex64 = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var hex40 = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

type State struct {
	Enabled        bool   `json:"enabled"`
	Checking       bool   `json:"checking"`
	CurrentVersion string `json:"current_version"`
	Available      bool   `json:"available"`
	Version        string `json:"version,omitempty"`
	Notes          string `json:"notes,omitempty"`
	CheckedAt      string `json:"checked_at,omitempty"`
	Error          string `json:"error,omitempty"`
	Installing     bool   `json:"installing"`
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type releaseResponse struct {
	Tag        string         `json:"tag_name"`
	Body       string         `json:"body"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type releaseManifest struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	File        string `json:"file"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	EXEIdentity struct {
		Tag    string `json:"tag"`
		Commit string `json:"commit"`
		Dirty  bool   `json:"dirty"`
	} `json:"exe_identity"`
}

type availableRelease struct {
	Version     string
	ManifestURL string
	SetupURL    string
}

type Manager struct {
	mu        sync.RWMutex
	state     State
	release   availableRelease
	client    *http.Client
	latestURL string
	dataRoot  string
	enabled   func() bool
	launch    func(string) error
	changed   func(State)
	now       func() time.Time
	lastCheck time.Time
	cancel    context.CancelFunc
}

type Options struct {
	CurrentVersion string
	DataRoot       string
	LatestURL      string
	Client         *http.Client
	Enabled        func() bool
	Launch         func(string) error
	Changed        func(State)
}

func New(options Options) *Manager {
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second, CheckRedirect: secureRedirect}
	}
	latest := strings.TrimSpace(options.LatestURL)
	if latest == "" {
		latest = LatestReleaseURL
	}
	enabled := options.Enabled
	if enabled == nil {
		enabled = func() bool { return true }
	}
	launch := options.Launch
	if launch == nil {
		launch = func(path string) error { return exec.Command(path, "--install", "--quiet").Start() }
	}
	manager := &Manager{client: client, latestURL: latest, dataRoot: options.DataRoot, enabled: enabled, launch: launch, changed: options.Changed, now: time.Now}
	manager.state = State{Enabled: enabled(), CurrentVersion: normalizeVersion(options.CurrentVersion)}
	return manager
}

func secureRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("too many redirects")
	}
	if len(via) > 0 && strings.EqualFold(via[0].URL.Scheme, "https") && !strings.EqualFold(request.URL.Scheme, "https") {
		return errors.New("HTTPS download redirected to a non-HTTPS URL")
	}
	return nil
}

func (m *Manager) Start(parent context.Context) {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	m.mu.Unlock()
	go func() {
		m.refreshEnabled()
		if m.enabled() {
			_ = m.Check(ctx)
		}
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.refreshEnabled()
				if m.enabled() {
					_ = m.Check(ctx)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.mu.Unlock()
}

func (m *Manager) State() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func (m *Manager) ConfigChanged(ctx context.Context) {
	wasEnabled := m.State().Enabled
	m.refreshEnabled()
	if !wasEnabled && m.enabled() {
		go func() { _ = m.Check(ctx) }()
	}
}

func (m *Manager) refreshEnabled() {
	m.mu.Lock()
	m.state.Enabled = m.enabled()
	if !m.state.Enabled {
		m.state.Checking = false
		m.state.Available = false
		m.state.Error = ""
		m.release = availableRelease{}
	}
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *Manager) Check(ctx context.Context) error {
	started, err := m.beginCheck(0)
	if err != nil || !started {
		return err
	}
	return m.finishCheck(ctx)
}

// TriggerIfStale starts a passive check without blocking the caller. The
// timestamp is reserved before the goroutine starts, so simultaneous window
// attaches cannot both pass the interval gate.
func (m *Manager) TriggerIfStale(parent context.Context, minimumInterval time.Duration) bool {
	started, err := m.beginCheck(minimumInterval)
	if err != nil || !started {
		return false
	}
	go func() {
		ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
		defer cancel()
		_ = m.finishCheck(ctx)
	}()
	return true
}

func (m *Manager) beginCheck(minimumInterval time.Duration) (bool, error) {
	if !m.enabled() {
		m.refreshEnabled()
		return false, nil
	}
	now := m.now()
	m.mu.Lock()
	if m.state.Checking {
		m.mu.Unlock()
		return false, errors.New("update check is already running")
	}
	if minimumInterval > 0 && !m.lastCheck.IsZero() && now.Sub(m.lastCheck) < minimumInterval {
		m.mu.Unlock()
		return false, nil
	}
	m.state.Enabled, m.state.Checking, m.state.Error = true, true, ""
	m.lastCheck = now
	checking := m.state
	m.mu.Unlock()
	m.publish(checking)
	return true, nil
}

func (m *Manager) finishCheck(ctx context.Context) error {
	release, err := m.fetchRelease(ctx)
	m.mu.Lock()
	// The operator may turn checks off while the one in-flight request is
	// finishing. Discard that response so a late result cannot repopulate the
	// strip or leave an install action available behind a disabled switch.
	if !m.enabled() {
		m.state.Enabled = false
		m.state.Checking = false
		m.state.Available = false
		m.state.Error = ""
		m.release = availableRelease{}
		state := m.state
		m.mu.Unlock()
		m.publish(state)
		return nil
	}
	m.state.Checking = false
	m.state.CheckedAt = m.now().UTC().Format(time.RFC3339)
	if err != nil {
		m.state.Error = err.Error()
		m.state.Available = false
		m.release = availableRelease{}
	} else {
		m.state.Error = ""
		m.state.Version = release.Version
		m.state.Notes = release.Notes
		m.state.Available = release.Available
		m.release = release.availableRelease
	}
	state := m.state
	m.mu.Unlock()
	m.publish(state)
	return err
}

type fetchedRelease struct {
	availableRelease
	Notes     string
	Available bool
}

func (m *Manager) fetchRelease(ctx context.Context) (fetchedRelease, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.latestURL, nil)
	if err != nil {
		return fetchedRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "Agent_b update checker")
	response, err := m.client.Do(request)
	if err != nil {
		return fetchedRelease{}, fmt.Errorf("update check failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fetchedRelease{}, fmt.Errorf("update check failed: HTTP %d", response.StatusCode)
	}
	var remote releaseResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxMetadata+1)).Decode(&remote); err != nil {
		return fetchedRelease{}, fmt.Errorf("update check response: %w", err)
	}
	if remote.Draft || remote.Prerelease {
		return fetchedRelease{}, errors.New("latest release is not a published stable release")
	}
	version := normalizeVersion(remote.Tag)
	if _, ok := semanticVersion(version); !ok {
		return fetchedRelease{}, fmt.Errorf("latest release has invalid version %q", remote.Tag)
	}
	result := fetchedRelease{availableRelease: availableRelease{Version: version}, Notes: firstLine(remote.Body)}
	for _, asset := range remote.Assets {
		switch asset.Name {
		case manifestName:
			result.ManifestURL = asset.URL
		case setupName:
			result.SetupURL = asset.URL
		}
	}
	if result.ManifestURL == "" || result.SetupURL == "" {
		return fetchedRelease{}, errors.New("latest release is missing release.json or Agent_b-setup.exe")
	}
	if err := m.validateAssetURL(result.ManifestURL); err != nil {
		return fetchedRelease{}, fmt.Errorf("release.json URL: %w", err)
	}
	if err := m.validateAssetURL(result.SetupURL); err != nil {
		return fetchedRelease{}, fmt.Errorf("setup URL: %w", err)
	}
	current, currentOK := semanticVersion(m.state.CurrentVersion)
	remoteVersion, _ := semanticVersion(version)
	result.Available = currentOK && compareVersion(remoteVersion, current) > 0
	return result, nil
}

func (m *Manager) validateAssetURL(raw string) error {
	asset, err := url.Parse(raw)
	if err != nil || asset.Hostname() == "" {
		return errors.New("must be an absolute URL")
	}
	latest, _ := url.Parse(m.latestURL)
	if strings.EqualFold(latest.Scheme, "https") && !strings.EqualFold(asset.Scheme, "https") {
		return errors.New("must use HTTPS")
	}
	if strings.EqualFold(latest.Scheme, "http") && !strings.EqualFold(asset.Hostname(), latest.Hostname()) {
		return errors.New("fixture assets must use the fixture host")
	}
	return nil
}

func (m *Manager) Install(ctx context.Context) (string, error) {
	m.mu.Lock()
	if m.state.Installing {
		m.mu.Unlock()
		return "", errors.New("update installation is already running")
	}
	release := m.release
	if !m.state.Available || release.Version == "" {
		m.mu.Unlock()
		return "", errors.New("no verified update is available")
	}
	m.state.Installing, m.state.Error = true, ""
	state := m.state
	m.mu.Unlock()
	m.publish(state)

	path, err := m.download(ctx, release)
	if err == nil {
		err = m.launch(path)
	}
	m.mu.Lock()
	m.state.Installing = false
	if err != nil {
		m.state.Error = err.Error()
	}
	state = m.state
	m.mu.Unlock()
	m.publish(state)
	if err != nil {
		return "", err
	}
	return path, nil
}

func (m *Manager) download(ctx context.Context, release availableRelease) (string, error) {
	manifestBytes, err := m.getBounded(ctx, release.ManifestURL, maxMetadata)
	if err != nil {
		return "", fmt.Errorf("download release.json: %w", err)
	}
	var manifest releaseManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return "", fmt.Errorf("release.json: %w", err)
	}
	if normalizeVersion(manifest.Version) != release.Version || manifest.File != setupName || !hex64.MatchString(manifest.SHA256) || manifest.Bytes < 1 || !hex40.MatchString(manifest.Commit) {
		return "", errors.New("release.json identity is invalid")
	}
	if normalizeVersion(manifest.EXEIdentity.Tag) != release.Version || !strings.EqualFold(manifest.EXEIdentity.Commit, manifest.Commit) || manifest.EXEIdentity.Dirty {
		return "", errors.New("release.json executable identity does not match the release")
	}
	setupBytes, err := m.getBounded(ctx, release.SetupURL, manifest.Bytes)
	if err != nil {
		return "", fmt.Errorf("download Agent_b-setup.exe: %w", err)
	}
	if int64(len(setupBytes)) != manifest.Bytes {
		return "", fmt.Errorf("setup size mismatch: got %d, want %d", len(setupBytes), manifest.Bytes)
	}
	digest := sha256.Sum256(setupBytes)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), manifest.SHA256) {
		return "", errors.New("setup SHA-256 mismatch")
	}
	dir := filepath.Join(m.dataRoot, "updates", release.Version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create update directory: %w", err)
	}
	final := filepath.Join(dir, setupName)
	part := final + ".part"
	if err := os.WriteFile(part, setupBytes, 0o700); err != nil {
		_ = os.Remove(part)
		return "", fmt.Errorf("write verified setup: %w", err)
	}
	if err := os.Remove(final); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(part)
		return "", fmt.Errorf("replace prior verified setup: %w", err)
	}
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part)
		return "", fmt.Errorf("publish verified setup: %w", err)
	}
	return final, nil
}

func (m *Manager) getBounded(ctx context.Context, raw string, max int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Agent_b update downloader")
	response, err := m.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("response exceeds %d bytes", max)
	}
	return data, nil
}

func (m *Manager) publish(state State) {
	if m.changed != nil {
		m.changed(state)
	}
}

func firstLine(value string) string {
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(strings.TrimLeft(line, "#*- ")); line != "" {
			if len(line) > 240 {
				return line[:240]
			}
			return line
		}
	}
	return ""
}

func normalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	if value != "" && value[0] != 'v' {
		value = "v" + value
	}
	return value
}

func semanticVersion(value string) ([3]int, bool) {
	var result [3]int
	parts := strings.Split(strings.TrimPrefix(normalizeVersion(value), "v"), ".")
	if len(parts) != 3 {
		return result, false
	}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return result, false
		}
		result[index] = number
	}
	return result, true
}

func compareVersion(left, right [3]int) int {
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}
