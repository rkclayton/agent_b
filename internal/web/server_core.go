package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/detection"
	"harness/internal/events"
	"harness/internal/hardening"
	"harness/internal/memory"
	"harness/internal/modelinstall"
	"harness/internal/ocr"
	"harness/internal/operatorfiles"
	"harness/internal/profiles"
	"harness/internal/projection"
	"harness/internal/serviceaccount"
	"harness/internal/session"
	"harness/internal/signing"
	"harness/internal/stats"
	"harness/internal/tools"
	"harness/internal/updater"
	"harness/internal/worker"
	workspaceinfo "harness/internal/workspace"
)

type Server struct {
	mu                sync.RWMutex
	cfg               *config.Config
	configPath        string
	roots             RuntimeRoots
	bus               *events.Bus
	registry          *session.Registry
	webDir            string
	worker            *worker.Driver
	workerStates      map[string]workerState
	scheduler         *agent.Scheduler
	runner            *agent.Runner
	prompt            *agent.PromptRenderer
	replay            *projection.Replay
	projector         *projection.Store
	writers           *events.Writers
	credential        *credential.Store
	notifications     notificationManager
	notificationStore *credential.Store
	updater           *updater.Manager
	shell             *tools.Shell
	account           serviceaccount.Manager
	hardening         hardening.Manager
	hardeningMu       sync.RWMutex
	hardeningOp       hardeningOperation
	signing           signing.Manager
	signingMu         sync.Mutex
	signingStatus     signing.Status
	shellTest         func(context.Context) (string, error)
	accountMu         sync.Mutex
	mutationToken     string
	operatorChangeMu  sync.Mutex
	operatorMu        sync.Mutex
	operatorEnabled   bool
	operatorExpires   string
	operatorTimer     operatorTimer
	operatorEpoch     uint64
	operatorRequest   func(*http.Request) error
	operatorNow       func() time.Time
	operatorAfter     func(time.Duration, func()) operatorTimer
	browserSession    string
	openFolder        func(string) error
	openFile          func(string) error
	extractClient     *http.Client
	ocrExtract        func(string) (string, error)
	ocrPDF            func(string, int) (string, error)
	detectLocal       func(context.Context, string) (any, error)
	workspaceState    *workspaceinfo.Manager
	memoryState       *memory.Manager
	reflection        *reflectionState
	proposals         *proposalOffers
	speech            speechProbe
	speechCommand     func(context.Context, string, ...string) *exec.Cmd
	modelInstaller    *modelinstall.Manager
	measureMu         sync.RWMutex
	measurements      map[string]measureState
	measureCancels    map[string]context.CancelFunc
	statsState        *stats.Manager
	operatorFiles     *operatorfiles.Manager
	profiles          *profiles.Manager
	profileChanged    func(string) error
	probeMu           sync.Mutex
	probeCancels      map[string]*probeRun
	// Item 2gy: how many inconclusive probes a connection has had in a row, which
	// is where it stands on the backoff ladder.
	probeRetries      map[string]int
	reachabilityMu    sync.Mutex
	reachability      map[string]*reachabilityRetry
	reachabilityAfter func(time.Duration, func()) operatorTimer
	navigationMu      sync.Mutex
	navigationIDs     map[string]time.Time
	agentConnectionMu sync.Mutex
	agentConnections  map[string]pendingAgentConnection
	tryAgentIdle      func(string) bool
	hostWindowAction  func(string) bool
	startedAt         string
}

type probeRun struct{ cancel context.CancelFunc }
type RuntimeRoots struct {
	Application string
	Data        string
	Profile     string
	Workspace   string
}

type operatorTimer interface {
	Stop() bool
}

func New(cfg *config.Config, path, webDir string, roots RuntimeRoots, bus *events.Bus) *Server {
	cfg.Shell.OperatorContext = false
	cfg.Shell.OperatorContextExpiresAt = ""
	server := &Server{
		cfg: cfg, configPath: path, webDir: webDir, roots: roots, bus: bus, mutationToken: newMutationToken(), browserSession: newMutationToken(),
		startedAt:       time.Now().UTC().Format(time.RFC3339),
		operatorRequest: requireOperatorHTTPClient,
		operatorNow:     time.Now,
		operatorAfter: func(duration time.Duration, fn func()) operatorTimer {
			return time.AfterFunc(duration, fn)
		},
		openFolder:   openContainingFolder,
		openFile:     openWithDefaultApplication,
		proposals:    newProposalOffers(),
		probeCancels: map[string]*probeRun{},
		reachability: map[string]*reachabilityRetry{},
		reachabilityAfter: func(duration time.Duration, fn func()) operatorTimer {
			return time.AfterFunc(duration, fn)
		},
		navigationIDs:    map[string]time.Time{},
		agentConnections: map[string]pendingAgentConnection{},
		measurements:     map[string]measureState{},
		measureCancels:   map[string]context.CancelFunc{},
		extractClient:    &http.Client{},
		ocrExtract:       ocr.Extract,
		ocrPDF:           ocr.ExtractPDF,
		detectLocal: func(ctx context.Context, account string) (any, error) {
			return detection.Local(ctx, filepath.Join(roots.Application, "scripts", "detect-local-capabilities.ps1"), account)
		},
	}
	server.initModelInstaller()
	return server
}
func (s *Server) SetRegistry(registry *session.Registry) {
	registry.SetPlansRoot(filepath.Join(s.profileRoot(), "plans"))
	s.registry = registry
}
func (s *Server) SetProfiles(manager *profiles.Manager) { s.profiles = manager }
func (s *Server) SetProfileChanged(change func(string) error) {
	s.profileChanged = change
}
func (s *Server) profileRoot() string {
	if s.roots.Profile != "" {
		return s.roots.Profile
	}
	return s.roots.Data
}
func (s *Server) SetWorkspaceState(manager *workspaceinfo.Manager, memories *memory.Manager) {
	s.workspaceState, s.memoryState = manager, memories
}
func (s *Server) SetStats(manager *stats.Manager)                 { s.statsState = manager }
func (s *Server) SetOperatorFiles(manager *operatorfiles.Manager) { s.operatorFiles = manager }
func (s *Server) SetReplay(replay *projection.Replay)             { s.replay = replay }
func (s *Server) SetProjection(projector *projection.Store, writers *events.Writers) {
	s.projector, s.writers = projector, writers
}
func (s *Server) SetShellSecurity(store *credential.Store, shell *tools.Shell) {
	s.credential = store
	s.shell = shell
	s.shellTest = shell.TestServiceAccount
}
func (s *Server) SetNotifications(manager notificationManager, store *credential.Store) {
	s.notifications, s.notificationStore = manager, store
}
func (s *Server) SetUpdater(manager *updater.Manager)                     { s.updater = manager }
func (s *Server) SetServiceAccountManager(manager serviceaccount.Manager) { s.account = manager }
func (s *Server) SetHardeningManager(manager hardening.Manager)           { s.hardening = manager }
func (s *Server) SetSigningManager(manager signing.Manager)               { s.signing = manager }
func (s *Server) SetHostWindowAction(action func(string) bool)            { s.hostWindowAction = action }
func (s *Server) SetRuntime(scheduler *agent.Scheduler, runner *agent.Runner, prompt *agent.PromptRenderer) {
	s.scheduler = scheduler
	s.runner = runner
	s.prompt = prompt
	if scheduler != nil {
		scheduler.SetAgentIdleCallback(s.applyPendingAgentConnection)
		s.tryAgentIdle = scheduler.TryAgentIdle
	}
	// The worker drives ordinary runs through the same scheduler, so it exists
	// only once there is one to drive.
	if scheduler != nil {
		s.worker = worker.New(s.bus, schedulerSubmitter{scheduler}, func() []*session.Session { return s.registry.List() })
		if runner != nil {
			s.worker.SetVerifier(runner)
		}
	}
	if runner != nil {
		runner.SetMessageLimitRecorder(s.recordObservedMessageLimit)
		runner.SetModelUnreachable(func(sessionID, connectionID string) {
			if scheduler != nil {
				scheduler.HoldModel(sessionID)
			}
			s.scheduleReachabilityProbe(connectionID)
		})
		runner.SetToolActivity(func(phase string) {
			s.touchOperatorContext("idle window reset: tool execution " + phase)
		})
	}
}

func (s *Server) recordObservedMessageLimit(connectionID string, limit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := *s.cfg
	next.Connections = append([]config.Connection(nil), s.cfg.Connections...)
	for i := range next.Connections {
		if next.Connections[i].ID != connectionID {
			continue
		}
		if next.Connections[i].Capabilities.ObservedMessageLimit == limit {
			return nil
		}
		next.Connections[i].Capabilities.ObservedMessageLimit = limit
		if err := next.Save(s.configPath); err != nil {
			return err
		}
		*s.cfg = next
		return nil
	}
	return fmt.Errorf("connection %q not found", connectionID)
}
func (s *Server) ConfigSnapshot() config.Config {
	s.mu.RLock()
	result := *s.cfg
	s.mu.RUnlock()
	s.operatorMu.Lock()
	result.Shell.OperatorContext = s.operatorEnabled
	result.Shell.OperatorContextExpiresAt = s.operatorExpires
	s.operatorMu.Unlock()
	return result
}
func (s *Server) Connection(id string) (*config.Connection, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Connection(id)
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.page)
	mux.HandleFunc("/chat", s.page)
	mux.HandleFunc("/plan", s.page)
	mux.HandleFunc("/setup", s.page)
	// Item 2fo: a page that names no icon still asks for /favicon.ico.
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(s.webDir, "assets", "Agent_b.ico"))
	})
	mux.Handle("/static/", revalidateStatic(http.StripPrefix("/static/", http.FileServer(http.Dir(s.webDir)))))
	mux.HandleFunc("/api/events", s.sse)
	mux.HandleFunc("/api/state", s.state)
	mux.HandleFunc("/api/browser-session", s.browserSessionEndpoint)
	mux.HandleFunc("/api/local-detection", s.localDetection)
	mux.HandleFunc("/api/reflection", s.replayGuard(s.reflectionEndpoint))
	mux.HandleFunc("/api/files/", s.file)
	mux.HandleFunc("/api/open-folder", s.openFileFolder)
	mux.HandleFunc("/api/open-file", s.openDeliveredFile)
	mux.HandleFunc("/api/attachments", s.replayGuard(s.attachments))
	mux.HandleFunc("/api/exchange-files", s.exchangeFiles)
	mux.HandleFunc("/api/operator-attachments", s.operatorAttachments)
	mux.HandleFunc("/api/operator-files", s.replayGuard(s.operatorFileState))
	mux.HandleFunc("/api/ui-errors", s.replayGuard(s.uiError))
	mux.HandleFunc("/api/navigation-starts", s.replayGuard(s.navigationStart))
	mux.HandleFunc("/api/navigation-measurements", s.replayGuard(s.navigationMeasurement))
	mux.HandleFunc("/api/navigation-suppressions", s.replayGuard(s.navigationSuppression))
	mux.HandleFunc("/api/sessions", s.replayGuard(s.sessions))
	mux.HandleFunc("/api/plans", s.replayGuard(s.plans))
	mux.HandleFunc("/api/plans/build", s.replayGuard(s.buildPlanRoute))
	mux.HandleFunc("/api/plan", s.replayGuard(s.planSurface))
	mux.HandleFunc("/api/plan/accept", s.replayGuard(s.planAccept))
	mux.HandleFunc("/api/plan/marker", s.replayGuard(s.planMarker))
	mux.HandleFunc("/api/plan/go", s.replayGuard(s.planGo))
	mux.HandleFunc("/api/plan/worker", s.replayGuard(s.planWorker))
	mux.HandleFunc("/api/sessions/", s.replayGuard(s.session))
	mux.HandleFunc("/api/workspaces", s.replayGuard(s.workspaces))
	mux.HandleFunc("/api/workspaces/", s.replayGuard(s.workspaceAction))
	mux.HandleFunc("/api/connections", s.connections)
	mux.HandleFunc("/api/connections/", s.replayGuard(s.connection))
	mux.HandleFunc("/api/profiles", s.replayGuard(s.profileEndpoint))
	mux.HandleFunc("/api/config", s.replayGuard(s.config))
	mux.HandleFunc("/api/notifications", s.replayGuard(s.notificationSettings))
	mux.HandleFunc("/api/update", s.replayGuard(s.updateEndpoint))
	mux.HandleFunc("/api/shell-credential", s.replayGuard(s.shellCredential))
	mux.HandleFunc("/api/service-account", s.replayGuard(s.serviceAccount))
	mux.HandleFunc("/api/hardening", s.replayGuard(s.hostHardening))
	mux.HandleFunc("/api/signing", s.replayGuard(s.codeSigning))
	mux.HandleFunc("/api/message", s.replayGuard(s.message))
	mux.HandleFunc("/api/stop", s.replayGuard(s.stop))
	mux.HandleFunc("/api/host-window", s.replayGuard(s.hostWindow))
	// Item 2ge: the composer microphone asks the host what it can do.
	mux.HandleFunc("/api/speech", s.speechHandler)
	mux.HandleFunc("/api/speech/stream", s.speechStreamHandler)
	mux.HandleFunc("/api/speech/stop", s.replayGuard(s.speechStopHandler))
	mux.HandleFunc("/api/model-install", s.replayGuard(s.modelInstall))
	mux.HandleFunc("/api/eval/measure", s.replayGuard(s.measureConnection))
	mux.HandleFunc("/api/approve", s.replayGuard(s.approve))
	mux.HandleFunc("/api/tools/", s.replayGuard(s.toggleTool))
	mux.HandleFunc("/api/stats/", s.replayGuard(s.stats))
	mux.HandleFunc("/api/agents/", s.replayGuard(s.agentAction))
	return s.securityHeaders(s.browserSessionGuard(s.mutationGuard(mux)))
}

func (s *Server) hardeningRequest(connectionID string) (hardening.Request, error) {
	s.mu.RLock()
	cfg := *s.cfg
	s.mu.RUnlock()
	if connectionID == "" {
		if agent, ok := cfg.Agent(cfg.DefaultAgentID()); ok {
			connectionID = agent.B
		}
	}
	var connection *config.Connection
	for index := range cfg.Connections {
		if cfg.Connections[index].ID == connectionID {
			value := cfg.Connections[index]
			connection = &value
			break
		}
	}
	if connection == nil {
		return hardening.Request{}, fmt.Errorf("model connection not found")
	}
	endpoint, err := url.Parse(connection.BaseURL)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return hardening.Request{}, fmt.Errorf("model connection base_url is invalid")
	}
	host := endpoint.Hostname()
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	if net.ParseIP(host) == nil && !validModelHostname(host) {
		return hardening.Request{}, fmt.Errorf("model connection host %q is not a valid hostname or IP address", host)
	}
	port := 0
	if endpoint.Port() != "" {
		if _, err := fmt.Sscanf(endpoint.Port(), "%d", &port); err != nil {
			return hardening.Request{}, fmt.Errorf("model connection port is invalid")
		}
	} else if endpoint.Scheme == "https" {
		port = 443
	} else {
		port = 80
	}
	if port < 1 || port > 65535 {
		return hardening.Request{}, fmt.Errorf("model connection port must be between 1 and 65535")
	}
	exchange, err := cfg.ResolvedExchangeFolder()
	if err != nil {
		return hardening.Request{}, err
	}
	return hardening.Request{
		AccountName: cfg.Shell.ServiceAccount.Account, ApplicationDirectory: s.roots.Application,
		DataDirectory: s.roots.Data, WorkspaceDirectory: s.roots.Workspace, ExchangeDirectory: exchange,
		ModelAddress: host, ModelPort: port, AllowLocalNetwork: cfg.Shell.AllowLocalNetwork,
		LocalSubnets:       append([]string(nil), cfg.Shell.ConfirmedLocalSubnets...),
		AllowedModelRanges: append([]string(nil), cfg.Shell.AllowedModelRanges...),
	}, nil
}

func validModelHostname(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '-' {
				return false
			}
		}
	}
	return true
}

func newMutationToken() string {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		panic("generate HTTP mutation token: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func (s *Server) mutationGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || r.URL.Path == "/api/browser-session" {
			next.ServeHTTP(w, r)
			return
		}
		provided := r.Header.Get("X-AgentB-Mutation-Token")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.mutationToken)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameRequestOrigin(origin, r.Host) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

const browserSessionCookie = "agentb_browser"

func (s *Server) browserSessionGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protectedRead := r.Method == http.MethodGet && (r.URL.Path == "/api/state" || r.URL.Path == "/api/events")
		mutation := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		if (protectedRead || mutation) && r.URL.Path != "/api/browser-session" {
			cookie, err := r.Cookie(browserSessionCookie)
			if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(s.browserSession)) != 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) browserSessionEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	provided := r.Header.Get("X-AgentB-Mutation-Token")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(s.mutationToken)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !sameRequestOrigin(origin, r.Host) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: browserSessionCookie, Value: s.browserSession, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// BrowserBootstrapToken is carried only in the native host's URL fragment.
// Fragments are not included in HTTP requests, logs, or Referer headers.
func (s *Server) BrowserBootstrapToken() string { return s.mutationToken }

func sameRequestOrigin(origin, requestHost string) bool {
	parsed, err := url.Parse(origin)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && strings.EqualFold(parsed.Host, requestHost)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) replayGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.replay != nil && r.Method != http.MethodGet {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "replay mode"})
			return
		}
		next(w, r)
	}
}

func mergeConfig(dst, src map[string]any) {
	for key, value := range src {
		if key == "connections" {
			incoming, _ := value.([]any)
			existing, _ := dst[key].([]any)
			byID := map[string]map[string]any{}
			order := []string{}
			for _, raw := range existing {
				item, _ := raw.(map[string]any)
				id, _ := item["id"].(string)
				byID[id] = item
				order = append(order, id)
			}
			for _, raw := range incoming {
				item, _ := raw.(map[string]any)
				id, _ := item["id"].(string)
				if id == "" {
					continue
				}
				if item["api_key"] == "•••• set" {
					delete(item, "api_key")
				}
				if byID[id] == nil {
					byID[id] = map[string]any{"id": id}
					order = append(order, id)
				}
				mergeConfig(byID[id], item)
			}
			out := make([]any, 0, len(order))
			for _, id := range order {
				out = append(out, byID[id])
			}
			dst[key] = out
			continue
		}
		child, childOK := value.(map[string]any)
		target, targetOK := dst[key].(map[string]any)
		if childOK && targetOK {
			mergeConfig(target, child)
		} else {
			dst[key] = value
		}
	}
}
func notBuilt(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 501, map[string]string{"error": "not built yet (prompt 4)"})
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(w, 400, err.Error(), "body")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message, field string) {
	writeJSON(w, status, map[string]string{"error": message, "field": field})
}
func method(w http.ResponseWriter) { writeError(w, 405, "method not allowed", "method") }
