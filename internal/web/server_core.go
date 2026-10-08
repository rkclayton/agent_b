package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"harness/internal/agent"
	"harness/internal/chatstore"
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
	"harness/internal/recorder"
	"harness/internal/serviceaccount"
	"harness/internal/session"
	"harness/internal/signing"
	"harness/internal/skills"
	"harness/internal/stats"
	"harness/internal/tools"
	"harness/internal/updater"
	"harness/internal/worker"
	workspaceinfo "harness/internal/workspace"
)

type Server struct {
	mu         sync.RWMutex
	cfg        *config.Config
	configPath string
	roots      RuntimeRoots
	bus        *events.Bus
	// Item 2jg: the telemetry sender and its subscription, or nothing at all.
	telemetry telemetryHost
	// Item 2pw: the flight recorder, fed by the bus whatever the switch says.
	recorder *recorder.Recorder
	app      *recorder.App
	// Item 2kq: the broker client, or nothing when broker.url is empty.
	brokerMu           sync.RWMutex
	broker             BrokerHost
	registry           *session.Registry
	webDir             string
	worker             *worker.Driver
	workerStates       map[string]workerState
	scheduler          *agent.Scheduler
	runner             *agent.Runner
	prompt             *agent.PromptRenderer
	replay             *projection.Replay
	projector          *projection.Store
	writers            *events.Writers
	credential         *credential.Store
	notifications      notificationManager
	notificationStore  *credential.Store
	updater            *updater.Manager
	shell              *tools.Shell
	account            serviceaccount.Manager
	hardening          hardening.Manager
	hardeningMu        sync.RWMutex
	hardeningOp        hardeningOperation
	signing            signing.Manager
	signingMu          sync.Mutex
	signingStatus      signing.Status
	shellTest          func(context.Context) (string, error)
	accountMu          sync.Mutex
	credentialRejected bool
	mutationToken      string
	// Item 2mx: submissions already answered, so a phone's retry does not start a
	// second run.
	submissions      *idempotentSubmissions
	operatorChangeMu sync.Mutex
	operatorMu       sync.Mutex
	operatorEnabled  bool
	operatorExpires  string
	operatorTimer    operatorTimer
	operatorEpoch    uint64
	operatorRequest  func(*http.Request) error
	operatorNow      func() time.Time
	operatorAfter    func(time.Duration, func()) operatorTimer
	browserSession   string
	openFolder       func(string) error
	openFile         func(string) error
	extractClient    *http.Client
	ocrExtract       func(string) (string, error)
	ocrPDF           func(context.Context, string, func(int, int)) (string, int, int, bool, error)
	attachmentMu     sync.Mutex
	attachmentStops  map[string]context.CancelFunc
	detectLocal      func(context.Context, string) (any, error)
	workspaceState   *workspaceinfo.Manager
	memoryState      *memory.Manager
	reflection       *reflectionState
	proposals        *proposalOffers
	speech           speechProbe
	speechCommand    func(context.Context, string, ...string) *exec.Cmd
	modelInstaller   *modelinstall.Manager
	measureMu        sync.RWMutex
	measurements     map[string]measureState
	measureCancels   map[string]context.CancelFunc
	statsState       *stats.Manager
	operatorFiles    *operatorfiles.Manager
	profiles         *profiles.Manager
	profileChanged   func(string) error
	probeMu          sync.Mutex
	probeCancels     map[string]*probeRun
	// Item 2gy: how many inconclusive probes a connection has had in a row, which
	// is where it stands on the backoff ladder.
	probeRetries         map[string]int
	reachabilityMu       sync.Mutex
	reachability         map[string]*reachabilityRetry
	reachabilityAfter    func(time.Duration, func()) operatorTimer
	healthMu             sync.Mutex
	health               map[string]connectionHealth
	healthEvery          time.Duration
	navigationMu         sync.Mutex
	requestWatchdogAfter time.Duration
	requestWatchdogLog   func(string)
	navigationIDs        map[string]time.Time
	agentConnectionMu    sync.Mutex
	agentConnections     map[string]pendingAgentConnection
	tryAgentIdle         func(string) bool
	hostWindowAction     func(string, string) bool
	startedAt            string
	chatMu               sync.RWMutex
	chatStore            *chatstore.Store
	chatEntries          []chatstore.Entry
	chatWatchMu          sync.Mutex
	chatCancel           context.CancelFunc
	chatDone             chan struct{}
}

type probeRun struct{ cancel context.CancelFunc }

const appRequestWatchdogDelay = 30 * time.Second

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
		cfg: cfg, configPath: path, webDir: webDir, roots: roots, bus: bus, mutationToken: newMutationToken(), browserSession: newMutationToken(), submissions: newIdempotentSubmissions(),
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
		attachmentStops:  map[string]context.CancelFunc{},
		ocrExtract:       ocr.Extract,
		ocrPDF:           ocr.ExtractPDF,
		detectLocal: func(ctx context.Context, account string) (any, error) {
			return detection.Local(ctx, filepath.Join(roots.Application, "scripts", "detect-local-capabilities.ps1"), account)
		},
	}
	server.recorder = recorder.New()
	server.recorder.SetSink(server.queueRunTelemetry)
	// Item 2q7: the app around the runs, an hour at a time.
	server.app = recorder.NewApp(time.Now())
	server.app.SetSink(server.queueRunTelemetry)
	server.app.Resources = server.resourceProbe
	go server.app.Run(context.Background())
	if bus != nil {
		bus.SetObserver(func(event events.Event) { server.recorder.Observe(event); server.app.Observe(event) })
	}
	_ = removeLegacyPhoneAccess(server.profileRoot())
	server.initModelInstaller()
	return server
}

func removeLegacyPhoneAccess(root string) error {
	store, err := credential.NewNamed(root, "web-push-vapid")
	if err != nil {
		return err
	}
	for _, path := range []string{filepath.Join(root, "phone-push.json"), store.Path()} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
func (s *Server) SetRegistry(registry *session.Registry) {
	registry.SetPlansRoot(filepath.Join(s.profileRoot(), "plans"))
	registry.SetSkillsRoot(filepath.Join(s.profileRoot(), "skills"))
	registry.SetSkillStateRoot(filepath.Join(s.profileRoot(), "skill-state"))
	if err := skills.EnsureIncluded(filepath.Join(s.profileRoot(), "skills")); err != nil {
		log.Printf("included skills are unavailable: %v", err)
	}
	s.registry = registry
	s.chatWatchMu.Lock()
	defer s.chatWatchMu.Unlock()
	s.stopChatTrackingLocked()
	s.chatStore = chatstore.New(filepath.Join(s.profileRoot(), "chats"))
	s.archiveIdleChats(registry)
	s.startChatTrackingLocked(registry)
}

func (s *Server) archiveIdleChats(registry *session.Registry) {
	if s.chatStore == nil {
		return
	}
	protected := map[string]bool{}
	for _, item := range registry.List() {
		protected[item.ID] = item.IsRunning()
	}
	changed, err := s.chatStore.AutoArchive(time.Now(), protected)
	if err != nil {
		log.Printf("auto-archive chats: %v", err)
		return
	}
	for _, id := range changed {
		if _, ok := registry.Get(id); ok {
			_ = registry.Archive(id)
			if s.projector != nil {
				s.projector.Delete(id)
			}
			s.bus.Publish(events.New(events.ChatDeleted, "", "", map[string]any{"session_id": id, "archived": true}))
		}
	}
}

func (s *Server) stopChatTrackingLocked() {
	if s.chatCancel != nil {
		s.chatCancel()
	}
	if s.chatDone != nil {
		<-s.chatDone
	}
	s.chatCancel, s.chatDone = nil, nil
}

func (s *Server) startChatTrackingLocked(registry *session.Registry) {
	ctx, cancel := context.WithCancel(context.Background())
	s.chatCancel = cancel
	done := make(chan struct{})
	s.chatDone = done
	go func() {
		defer close(done)
		_ = s.chatStore.Watch(ctx, func(entries []chatstore.Entry) {
			s.chatMu.Lock()
			s.chatEntries = entries
			s.chatMu.Unlock()
			registry.ReconcileChatHomes(entries)
		})
	}()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.archiveIdleChats(registry)
			}
		}
	}()
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
func (s *Server) SetHostWindowAction(action func(string, string) bool)    { s.hostWindowAction = action }
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
		runner.SetSkillHost(s)
		runner.SetMessageLimitRecorder(s.recordObservedMessageLimit)
		runner.SetByteLimitRecorder(s.recordObservedByteLimit)
		runner.SetModelUnreachable(func(sessionID, connectionID string) {
			if scheduler != nil {
				scheduler.HoldModel(sessionID)
			}
			s.scheduleReachabilityProbe(connectionID)
			s.setConnectionHealth(connectionID, connectionHealth{Lamp: "alarm", Word: "unreachable"})
		})
		// Item 2px (e): a real answer is the best check there is; it clears red
		// and keeps an amber that is about the model or the window.
		runner.SetModelAnswered(func(connectionID string) {
			if current := s.connectionHealthState()[connectionID]; current.Lamp != "amber" {
				s.setConnectionHealth(connectionID, connectionHealth{Lamp: "ready", Word: "ready", Window: current.Window})
			}
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
		next.Connections[i].StoreActiveModel()
		if err := s.saveMachineConfig(next); err != nil {
			return err
		}
		*s.cfg = next
		return nil
	}
	return fmt.Errorf("connection %q not found", connectionID)
}

// Item 2l8: the byte cap a connection has been refused by is remembered the same
// way its message cap is, so a chat that was over the limit becomes sendable at
// its next turn instead of after another refusal.
func (s *Server) recordObservedByteLimit(connectionID string, limit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := *s.cfg
	next.Connections = append([]config.Connection(nil), s.cfg.Connections...)
	for i := range next.Connections {
		if next.Connections[i].ID != connectionID {
			continue
		}
		if next.Connections[i].Capabilities.ObservedByteLimit == limit {
			return nil
		}
		next.Connections[i].Capabilities.ObservedByteLimit = limit
		kept := next.Connections[i].Capabilities.Findings[:0]
		for _, finding := range next.Connections[i].Capabilities.Findings {
			if !strings.HasPrefix(finding, "size limit:") {
				kept = append(kept, finding)
			}
		}
		next.Connections[i].Capabilities.Findings = kept
		if limit > 0 {
			next.Connections[i].Capabilities.Findings = append(kept, fmt.Sprintf("size limit: %d bytes observed %s", limit, time.Now().Format("2006-01-02")))
		}
		next.Connections[i].StoreActiveModel()
		if err := s.saveMachineConfig(next); err != nil {
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

func normalizeTrustedFolders(cfg *config.Config, source string) error {
	now, seen, out := time.Now().UTC().Format(time.RFC3339), map[string]bool{}, make([]config.TrustedFolder, 0, len(cfg.Shell.TrustedFolders))
	for _, entry := range cfg.Shell.TrustedFolders {
		if strings.TrimSpace(entry.Path) == "" {
			return fmt.Errorf("folder is required")
		}
		real, err := session.RealPath(strings.TrimSpace(entry.Path))
		if err != nil {
			return err
		}
		key := strings.ToLower(real)
		if seen[key] {
			continue
		}
		seen[key] = true
		entry.Path = real
		if entry.Source == "" {
			entry.Source = source
		}
		if entry.AddedAt == "" {
			entry.AddedAt = now
		}
		out = append(out, entry)
	}
	cfg.Shell.TrustedFolders = out
	return nil
}

func (s *Server) TrustFolders(paths []string) error {
	s.mu.Lock()
	next := *s.cfg
	next.Shell.TrustedFolders = append([]config.TrustedFolder(nil), s.cfg.Shell.TrustedFolders...)
	for _, path := range paths {
		next.Shell.TrustedFolders = append(next.Shell.TrustedFolders, config.TrustedFolder{Path: path})
	}
	if err := normalizeTrustedFolders(&next, "card"); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := next.Validate(); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.saveMachineConfig(next); err != nil {
		s.mu.Unlock()
		return err
	}
	*s.cfg = next
	s.mu.Unlock()
	if s.runner != nil {
		s.runner.Configure(s.ConfigSnapshot())
	}
	s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": s.ConfigSnapshot().Masked()}))
	return nil
}
func (s *Server) Connection(id string) (*config.Connection, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Connection(id)
}
func (s *Server) Handler() http.Handler {
	return s.requestWatchdog(s.securityHeaders(retiredPhoneRoutes(s.browserSessionGuard(s.mutationGuard(s.routes())))))
}

func (s *Server) requestWatchdog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/events" || r.URL.Path == "/api/speech/stream" {
			next.ServeHTTP(w, r)
			return
		}
		after := s.requestWatchdogAfter
		if after <= 0 {
			after = appRequestWatchdogDelay
		}
		record := s.requestWatchdogLog
		if record == nil {
			record = func(value string) { log.Print(value) }
		}
		started := time.Now()
		timer := time.AfterFunc(after, func() {
			stack := make([]byte, (1<<20)-4096)
			count := runtime.Stack(stack, true)
			record(fmt.Sprintf("stuck app request route=%s open_for=%s waiting=%s", r.URL.Path, time.Since(started).Round(time.Millisecond), strings.TrimSpace(string(stack[:count]))))
		})
		defer timer.Stop()
		next.ServeHTTP(w, r)
	})
}

func retiredPhoneRoutes(next http.Handler) http.Handler {
	retired := map[string]bool{
		"/phone": true, "/phone-sw.js": true, "/static/phone.js": true, "/static/phone.css": true,
		"/api/phone/enrolment": true, "/api/phone/enrolment/redeem": true,
		"/api/phone/devices": true, "/api/phone/devices/revoke": true, "/api/phone/devices/revoke-all": true,
		"/api/phone/push": true, "/api/phone/push/subscriptions": true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retired[r.URL.Path] {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// routes is the mux itself. Item 2kq (c) needs it without the browser and mutation
// guards — those two are a browser page's CSRF and cookie, and the broker transport has
// neither; its authority is the pairing. Splitting it here is what lets the app-message
// dispatcher call THE SAME HANDLERS rather than a second implementation of them.
func (s *Server) routes() http.Handler {
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
	// Item 2mv: one read-only export of what this installation already knows.
	mux.HandleFunc("/api/diagnostics", s.diagnostics)
	mux.HandleFunc("/api/browser-session", s.browserSessionEndpoint)
	mux.HandleFunc("/api/broker/status", s.brokerStatus)
	mux.HandleFunc("/api/credentials", s.credentialsEndpoint)
	mux.HandleFunc("/api/broker", s.replayGuard(s.brokerAction))
	mux.HandleFunc("/api/local-detection", s.localDetection)
	mux.HandleFunc("/api/reflection", s.replayGuard(s.reflectionEndpoint))
	mux.HandleFunc("/api/files/", s.file)
	mux.HandleFunc("/api/open-folder", s.openFileFolder)
	mux.HandleFunc("/api/open-file", s.openDeliveredFile)
	mux.HandleFunc("/api/attachments", s.replayGuard(s.attachments))
	mux.HandleFunc("/api/attachments/stop", s.replayGuard(s.stopAttachment))
	mux.HandleFunc("/api/chat-mirror/take", s.replayGuard(s.takeMirrorHTTP))
	mux.HandleFunc("/api/exchange-files", s.exchangeFiles)
	mux.HandleFunc("/api/operator-attachments", s.operatorAttachments)
	mux.HandleFunc("/api/operator-files", s.replayGuard(s.operatorFileState))
	mux.HandleFunc("/api/ui-errors", s.replayGuard(s.uiError))
	mux.HandleFunc("/api/page-health", s.replayGuard(s.pageHealth))
	mux.HandleFunc("/api/header-state", s.replayGuard(s.headerState))
	mux.HandleFunc("/api/navigation-starts", s.replayGuard(s.navigationStart))
	mux.HandleFunc("/api/navigation-measurements", s.replayGuard(s.navigationMeasurement))
	mux.HandleFunc("/api/navigation-suppressions", s.replayGuard(s.navigationSuppression))
	mux.HandleFunc("/api/sessions", s.replayGuard(s.sessions))
	mux.HandleFunc("/api/chats/tree", s.replayGuard(s.chatTree))
	mux.HandleFunc("/api/chats/delete-all", s.replayGuard(s.deleteAllChats))
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
	mux.HandleFunc("/api/agent-memory/remove", s.replayGuard(s.agentMemoryRemove))
	// Item 2mw: the rows the Activity page builds from, and the two actions the
	// delete route did not have. Removal stays exactly where it was.
	mux.HandleFunc("/api/reflection-notes", s.replayGuard(s.reflectionNotes))
	mux.HandleFunc("/api/reflection-notes/confirm", s.replayGuard(s.reflectionNoteAction))
	mux.HandleFunc("/api/reflection-notes/restore", s.replayGuard(s.reflectionNoteAction))
	mux.HandleFunc("/api/reflection-notes/remove", s.replayGuard(s.reflectionNoteRemove))
	mux.HandleFunc("/api/standing-grants", s.replayGuard(s.standingGrants))
	mux.HandleFunc("/api/connections", s.connections)
	mux.HandleFunc("/api/connections/", s.replayGuard(s.connection))
	mux.HandleFunc("/api/profiles", s.replayGuard(s.profileEndpoint))
	mux.HandleFunc("/api/skills", s.replayGuard(s.skillsEndpoint))
	mux.HandleFunc("/api/config", s.replayGuard(s.config))
	mux.HandleFunc("/api/notifications", s.replayGuard(s.notificationSettings))
	mux.HandleFunc("/api/update", s.replayGuard(s.updateEndpoint))
	mux.HandleFunc("/api/shell-credential", s.replayGuard(s.shellCredential))
	mux.HandleFunc("/api/service-account", s.replayGuard(s.serviceAccount))
	mux.HandleFunc("/api/hardening", s.replayGuard(s.hostHardening))
	mux.HandleFunc("/api/message", s.replayGuard(s.message))
	// Item 2mx: one read a voice assistant can speak, for a run it just started.
	mux.HandleFunc("/api/runs/", s.replayGuard(s.runBrief))
	mux.HandleFunc("/api/stop", s.replayGuard(s.stop))
	mux.HandleFunc("/api/host-window", s.replayGuard(s.hostWindow))
	mux.HandleFunc("/api/sign-in-start", s.replayGuard(s.signInStart))
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
	return mux
}

// hardeningRequest is what the protection scripts are given. Item 2nx: NOTHING IN IT
// COMES FROM A MODEL CONNECTION any more — the service identity's outbound rule is
// loopback plus the operator's own approved ranges, so no host is resolved, nothing fails
// because a connection is unreachable, and switching connections changes nothing. The
// connectionID argument stays because callers pass one; it is no longer read.
func (s *Server) hardeningRequest(connectionID string) (hardening.Request, error) {
	s.mu.RLock()
	cfg := *s.cfg
	s.mu.RUnlock()
	exchange, err := cfg.ResolvedExchangeFolder()
	if err != nil {
		return hardening.Request{}, err
	}
	return hardening.Request{
		AccountName: cfg.Shell.ServiceAccount.Account, ApplicationDirectory: s.roots.Application,
		DataDirectory: s.roots.Data, WorkspaceDirectory: s.roots.Workspace, ExchangeDirectory: exchange,
		AllowLocalNetwork:  cfg.Shell.AllowLocalNetwork,
		LocalSubnets:       append([]string(nil), cfg.Shell.ConfirmedLocalSubnets...),
		AllowedModelRanges: append([]string(nil), cfg.Shell.AllowedModelRanges...),
	}, nil
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
		// Item 2nv (c): the credential listing joins the protected reads. It carries no
		// value, but it names what is stored and where it may go, and only the operator's
		// own page may see that.
		protectedRead := r.Method == http.MethodGet && (r.URL.Path == "/api/state" || r.URL.Path == "/api/events" || r.URL.Path == "/api/credentials")
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

// saveMachineConfig writes the machine's configuration file. Item 2m0: this is
// the ONE place the server writes it, and it writes the machine's own
// per-profile sections rather than the active profile's.
//
// Every caller here holds `*s.cfg`, which is the MERGE, so before this existed
// each of them wrote whichever profile was active into the machine's defaults as
// a side effect of changing a connection or the service identity. The audit at
// rel-1.21.0/W0 found nineteen such writers and only one that had any idea it
// was doing it.
//
// A caller whose write IS per-profile does not belong here: it calls
// saveProfileConfig instead, which puts the value where that scope lives.
func (s *Server) saveMachineConfig(cfg config.Config) error {
	if s.profiles == nil {
		return cfg.Save(s.configPath)
	}
	return s.profiles.SaveMachine(cfg)
}

// saveProfileConfig persists a change to a PER-PROFILE section, and writes the
// machine file too because a route may have changed both. Item 2m0 (b).
//
// The order matters: the profile's own file is written first, so that if the
// machine write fails the profile's value is already durable rather than lost.
func (s *Server) saveProfileConfig(cfg config.Config) error {
	if s.profiles == nil {
		return cfg.Save(s.configPath)
	}
	if err := s.profiles.SaveActive(); err != nil {
		return err
	}
	return s.profiles.SaveMachine(cfg)
}
