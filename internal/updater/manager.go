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

	"harness/internal/quietproc"
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
	// Item 2nh (a): the sequence, while it runs. Step names which of the five
	// stages is in hand, Line is the sentence the one wait element shows, and
	// Processed/Total make it determinate while the download is the stage — the
	// only part of an update whose size is known in advance.
	Step      string `json:"step,omitempty"`
	Line      string `json:"line,omitempty"`
	Processed int64  `json:"processed,omitempty"`
	Total     int64  `json:"total,omitempty"`
	// Item 2nh (b): what the LAST update did, so the instance that came back after
	// the restart can say so where the control was rather than flipping silently
	// back to "up to date".
	Outcome *Outcome `json:"outcome,omitempty"`
	// Item 2mk (b) and (c): HOW FAR BEHIND, and WHICH INSTALL. He was eleven
	// releases behind and learned it from a report; and on 2026-09-26 an install
	// wrote v1.22.0 to the per-user root while the window went on being served by
	// another instance entirely, with nothing naming either path.
	ReleasesBehind  int    `json:"releases_behind,omitempty"`
	ApplicationRoot string `json:"application_root,omitempty"`
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
	mu              sync.RWMutex
	state           State
	applicationRoot string
	release         availableRelease
	client          *http.Client
	latestURL       string
	dataRoot        string
	enabled         func() bool
	launch          func(string, string) error
	verify          func(context.Context, string) error
	changed         func(State)
	now             func() time.Time
	checkInterval   time.Duration
	lastCheck       time.Time
	cancel          context.CancelFunc
}

type Options struct {
	CurrentVersion string
	DataRoot       string
	// Item 2lh: the roots the ASKING instance lives in. The setup resolves the
	// operator own per-user locations when it is told nothing, so an instance that
	// does not pass its own roots updates over production whoever asked. These are
	// passed to the setup; an instance that cannot name them refuses to install.
	ApplicationRoot string
	WorkspaceRoot   string
	LatestURL       string
	Client          *http.Client
	Enabled         func() bool
	Launch          func(string, string) error
	VerifySignature func(context.Context, string) error
	Changed         func(State)
	CheckInterval   time.Duration
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
		application, data, workspace := options.ApplicationRoot, options.DataRoot, options.WorkspaceRoot
		launch = func(path, sessionID string) error {
			arguments, err := installArguments(application, data, workspace, sessionID)
			if err != nil {
				return err
			}
			// Item 2nf (d) and 2na (a): the setup is started with no console, so an
			// update never puts a window on the operator's screen and cannot leave one
			// behind when it fails.
			return quietproc.Quiet(exec.Command(path, arguments...)).Start()
		}
	}
	verify := options.VerifySignature
	if verify == nil {
		verify = verifySetupSignature
	}
	interval := options.CheckInterval
	if interval <= 0 {
		interval = time.Hour
	}
	manager := &Manager{client: client, latestURL: latest, dataRoot: options.DataRoot, applicationRoot: options.ApplicationRoot, enabled: enabled, launch: launch, verify: verify, changed: options.Changed, now: time.Now, checkInterval: interval}
	manager.state = State{Enabled: enabled(), CurrentVersion: normalizeVersion(options.CurrentVersion), ApplicationRoot: options.ApplicationRoot}
	// Item 2nh (b): THIS PROCESS MAY BE THE RESULT OF AN UPDATE. The installer
	// stopped the instance that pressed Update and started this one, so the only
	// account of what happened is the file the installer wrote. Read it once, here,
	// and the About row can state the outcome instead of saying nothing.
	manager.state.Outcome = outcomeFor(options.DataRoot, manager.state.CurrentVersion)
	if manager.state.Outcome != nil {
		// Item 2mk (c): the outcome names the install it is about. A version without
		// a path is what left the operator unable to tell which of two installs the
		// reports had been measuring.
		manager.state.Outcome.ApplicationRoot = options.ApplicationRoot
	}
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
		ticker := time.NewTicker(m.checkInterval)
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
		m.state.ReleasesBehind = 0
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
	// Item 2nh (b): the outcome stands UNTIL THE NEXT CHECK, and this is that check.
	m.state.Outcome = nil
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
		m.state.ReleasesBehind = 0
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
		m.state.ReleasesBehind = 0
		m.release = availableRelease{}
	} else {
		m.state.Error = ""
		m.state.Version = release.Version
		m.state.Notes = release.Notes
		m.state.Available = release.Available
		m.state.ReleasesBehind = release.Behind
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
	Behind    int
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
	if result.Available {
		result.Behind = m.countBehind(ctx, current)
	}
	return result, nil
}

// countBehind answers item 2mk (b): HOW FAR BEHIND, in releases, not in versions.
//
// The operator was eleven releases behind and learned it from a report rather than
// from the product. One published release is one thing he did not get, so the number
// is a count of published, stable releases newer than the one running — not a
// subtraction of version numbers, which would say "1" for a jump of eleven minors.
//
// It costs one more anonymous GET, and only when an update is already known to
// exist. A failure answers 0, which reads as "not said" rather than as "up to date":
// the available line is what tells him there is something, and this only sizes it.
func (m *Manager) countBehind(ctx context.Context, current [3]int) int {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseListURL(m.latestURL), nil)
	if err != nil {
		return 0
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "Agent_b update checker")
	response, err := m.client.Do(request)
	if err != nil {
		return 0
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0
	}
	var releases []releaseResponse
	if json.NewDecoder(io.LimitReader(response.Body, 4*maxMetadata+1)).Decode(&releases) != nil {
		return 0
	}
	behind := 0
	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		if version, ok := semanticVersion(normalizeVersion(release.Tag)); ok && compareVersion(version, current) > 0 {
			behind++
		}
	}
	return behind
}

// releaseListURL turns the latest-release URL into the list of releases beside it,
// so the fixture host a test points at is still the host this asks.
func releaseListURL(latest string) string {
	if strings.HasSuffix(latest, "/releases/latest") {
		return strings.TrimSuffix(latest, "/latest") + "?per_page=100"
	}
	if index := strings.LastIndex(latest, "/"); index > 0 {
		return latest[:index] + "/releases?per_page=100"
	}
	return latest
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

func (m *Manager) Install(ctx context.Context, sessionID string) (string, error) {
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
	m.state.Installing, m.state.Error, m.state.Outcome = true, "", nil
	state := m.state
	m.mu.Unlock()
	m.publish(state)
	m.setStep("downloading", "downloading Agent_b "+release.Version, 0, 0)

	path, err := m.download(ctx, release)
	if err == nil {
		m.setStep("installing", "starting the installer", 0, 0)
		err = m.launch(path, sessionID)
	}
	m.mu.Lock()
	m.state.Installing = false
	if err != nil {
		m.state.Error = err.Error()
		m.state.Step, m.state.Line, m.state.Processed, m.state.Total = "", "", 0, 0
	}
	state = m.state
	m.mu.Unlock()
	m.publish(state)
	if err != nil {
		return "", err
	}
	// Item 2mr (c): the setup LAUNCHING is not the setup SUCCEEDING. An installer
	// that ran and refused used to look identical to one that worked, because
	// nothing read back what it said. This watches the installer's own progress
	// file and puts its own sentence on state.Error if it fails; a successful
	// install replaces this process long before the watch ends.
	go m.watchInstallOutcome(m.now())
	return path, nil
}

// setStep names the stage in hand for the one wait element. It is a publish, not a
// promise: an update that dies between two steps leaves the last one it reached,
// which is more than the control used to say at any point.
func (m *Manager) setStep(step, line string, processed, total int64) {
	m.mu.Lock()
	m.state.Step, m.state.Line, m.state.Processed, m.state.Total = step, line, processed, total
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *Manager) download(ctx context.Context, release availableRelease) (string, error) {
	manifestBytes, err := m.getBounded(ctx, release.ManifestURL, maxMetadata, nil)
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
	setupBytes, err := m.getBounded(ctx, release.SetupURL, manifest.Bytes, func(read, total int64) {
		m.setStep("downloading", "downloading Agent_b "+release.Version, read, total)
	})
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
	m.setStep("verifying", "verifying the download's checksum and signature", 0, 0)
	if err := m.verify(ctx, final); err != nil {
		_ = os.Remove(final)
		return "", fmt.Errorf("setup Authenticode verification: %w", err)
	}
	return final, nil
}

func (m *Manager) getBounded(ctx context.Context, raw string, max int64, progress func(read, total int64)) ([]byte, error) {
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
	data, err := readReported(io.LimitReader(response.Body, max+1), max, progress)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("response exceeds %d bytes", max)
	}
	return data, nil
}

// readReported is io.ReadAll with a report of how far it has got. Item 2nh (a)
// wants the download determinate, and the release manifest already says the size,
// so this is the one part of an update that can honestly show a fraction.
func readReported(reader io.Reader, total int64, progress func(read, total int64)) ([]byte, error) {
	if progress == nil {
		return io.ReadAll(reader)
	}
	data := make([]byte, 0, 1<<20)
	buffer := make([]byte, 256*1024)
	reported := int64(-1)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			data = append(data, buffer[:count]...)
			// Report on a boundary rather than on every read: a publish per 256 KB
			// of a 13 MB download is fifty events, and a publish per read would be
			// thousands for the same twelve cells.
			if read := int64(len(data)); read != reported {
				reported = read
				progress(read, total)
			}
		}
		if err == io.EOF {
			return data, nil
		}
		if err != nil {
			return nil, err
		}
	}
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

// installArguments tells the setup where the asking instance lives. Item 2lh (a)
// and (b): production passes production roots and behaves exactly as before, while
// an instance that cannot name its own roots refuses rather than falling back to
// the operator location -- the fallback that made the launch half of this path
// impossible to gate without installing over production.
func installArguments(application, data, workspace, sessionID string) ([]string, error) {
	if strings.TrimSpace(application) == "" || strings.TrimSpace(data) == "" {
		return nil, errors.New("this instance cannot name its own application and data roots, so it will not install; install by hand from the release page")
	}
	// Item 2ll (a) and (d): BOTH arguments carry the same root. -DataDirectory is
	// the installer script's; --install-data is this process's, and it governs
	// the install log, the in-progress marker and the progress file. rel-1.14.0
	// passed only the first, so a disposable instance's update wrote its own
	// records into the operator's LocalAppData.
	arguments := []string{"--install", "--quiet", "--install-data", data, "-ApplicationDirectory", application, "-DataDirectory", data}
	// Item 2nf (b): THE WORKSPACE IS NO LONGER SENT. It was this instance's own
	// configured workspace, which for a real install sits inside the data root, and
	// every guard in the installer refused that: three Update attempts died in under a
	// second each. The installer resolves the workspace itself, as it does for a hand
	// run, so there is nothing here for it to reject. The two ROOTS stay, because
	// item 2lh's install-target assertion depends on them — without them a disposable
	// instance's update would install over the operator's own copy.
	_ = workspace
	if sessionID != "" {
		arguments = append(arguments, "--reopen-session", sessionID)
	}
	return arguments, nil
}
