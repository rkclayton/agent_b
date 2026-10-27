package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"harness/internal/chatstore"
	"harness/internal/config"
	contextmgr "harness/internal/context"
	"harness/internal/credential"
	"harness/internal/events"
	"harness/internal/projection"
	"harness/internal/recorder"
	"harness/internal/session"
	"harness/internal/tools"
	"harness/internal/updater"
)

func main() {
	if executable, err := os.Executable(); err == nil {
		_ = os.Setenv("AGENTB_EXE", executable)
	}
	if len(os.Args) > 1 && os.Args[1] == "browser" {
		os.Exit(runBrowserCommand(os.Args[2:]))
	}
	if setupExecutable(os.Args[0]) {
		if err := validateSetupArguments(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if err := startupElevationError(processIsElevated() && !allUsersInstallRequested(os.Args[0], os.Args[1:])); err != nil {
		log.Fatal(err)
	}
	// where the two minutes go, measured on every start.
	phases := newStartupPhases()
	startupTimer = phases
	flags := parseLaunchFlags()
	if runOneShotMode(flags) {
		return
	}
	// Item 2wm: the composition root. Each step takes what an earlier one made and keeps its
	// order and its log lines; the cleanups run in the order the defers they replace ran.
	a := &harnessApp{flags: flags, phases: phases}
	if !a.prepareLaunch() {
		return
	}
	a.loadConfig()
	if a.serveReplay() {
		return
	}
	defer a.close()
	a.openJournal()
	a.wireServices()
	a.wireTools()
	a.web.SetProfileChanged(a.onProfileChanged)
	a.restoreAtStartup()
	publishPendingSigning(a.paths.Data, a.registry, a.bus)
	if err := serve(a.cfg, a.web.Handler(), newLifetime(a.paths.Data, time.Now), a.paths.Application, a.web.BrowserBootstrapToken()); err != nil {
		startupFatal(err)
	}
}

type hostWindowActionSetter interface {
	SetHostWindowAction(func(string, string) bool)
}

// Every server can be asked to open its window by a later launch, so every
// server must also own that window's page actions. The process's initial flags
// decide when the window opens, not whether its buttons work afterward.
func attachHostWindowActions(server hostWindowActionSetter) {
	server.SetHostWindowAction(requestHostWindowAction)
}

func serviceIdentityStartupNotice(service config.ShellServiceAccount, status credential.Status) string {
	credentialState := "missing"
	if status.Stored {
		credentialState = "stored"
	}
	return fmt.Sprintf("service identity not set up: enabled=%t; account=%s; credential=%s", service.Enabled, service.Account, credentialState)
}

func publishServiceSetupInvitation(registry *session.Registry, bus *events.Bus) {
	var newest *session.Session
	for _, item := range registry.List() {
		if item.IsClosed() || item.Role == "c" {
			continue
		}
		if newest == nil || item.CreatedAt.After(newest.CreatedAt) || (item.CreatedAt.Equal(newest.CreatedAt) && item.ID > newest.ID) {
			newest = item
		}
	}
	if newest == nil {
		return
	}
	bus.Publish(events.New(events.ServiceIdentityUnavailable, newest.ID, "", map[string]any{
		"message": "Agent_b sets up its service identity now; Windows will ask once",
		"action":  "provision",
		"launch":  true,
	}))
}

func allUsersInstallRequested(executable string, arguments []string) bool {
	hasInstall, allUsers := setupExecutable(executable), false
	for _, argument := range arguments {
		switch flagName(argument) {
		case "install":
			hasInstall = true
		case "all-users":
			allUsers = true
		}
	}
	return hasInstall && allUsers
}

func setupExecutable(path string) bool {
	// Setup names are a Windows contract even when this pure parser is tested
	// on a Unix CI runner, where filepath.Base does not recognize backslashes.
	name := strings.ToLower(filepath.Base(strings.ReplaceAll(path, `\`, "/")))
	if name == "agent_b-setup.exe" {
		return true
	}
	const prefix = "agent_b-setup ("
	const suffix = ").exe"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	number := name[len(prefix) : len(name)-len(suffix)]
	if number == "" {
		return false
	}
	for _, digit := range number {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// AGENTB_UPDATE_SOURCE_URL lets an organisation publish the same release API
// shape at its own HTTPS endpoint. The downloaded release.json digest and the
// Authenticode verification remain mandatory on that path.
// AGENTB_UPDATE_FIXTURE_URL is an acceptance-only seam. It is deliberately
// limited to loopback so an inherited environment cannot redirect the product's
// release trust path to another public host.
func updateLatestURL() string {
	if raw := strings.TrimSpace(os.Getenv("AGENTB_UPDATE_SOURCE_URL")); raw != "" {
		endpoint, err := url.Parse(raw)
		if err == nil && endpoint.Scheme == "https" && endpoint.Hostname() != "" && endpoint.User == nil && endpoint.Fragment == "" {
			return endpoint.String()
		}
	}
	raw := strings.TrimSpace(os.Getenv("AGENTB_UPDATE_FIXTURE_URL"))
	if raw == "" {
		return updater.LatestReleaseURL
	}
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Fragment != "" {
		return updater.LatestReleaseURL
	}
	host := net.ParseIP(endpoint.Hostname())
	if host == nil || !host.IsLoopback() {
		return updater.LatestReleaseURL
	}
	return endpoint.String()
}

// AGENTB_NOTIFICATION_FIXTURE_URL is an acceptance-only seam. Like the update
// fixture above, it cannot name another machine or a public host.
func notificationFixtureURL() string {
	raw := strings.TrimSpace(os.Getenv("AGENTB_NOTIFICATION_FIXTURE_URL"))
	if raw == "" {
		return ""
	}
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return ""
	}
	host := net.ParseIP(endpoint.Hostname())
	if host == nil || !host.IsLoopback() {
		return ""
	}
	return endpoint.String()
}

// retainedIDFloor is the highest numeric id suffix a restored chat holds in a
// message id or a run id. Message and run ids come from counters that start at
// 1 in every process; without this floor the first run after a restart reused
// restoreProjections is the whole mechanism: read each chat's PROJECTED
// STATE when the journal it came from has not changed, and project only the journals
// that have.
//
// Measured on a copy of the user's own chats before this existed: projecting all
// 33 journals, 218,150,298 bytes, took 6.244s; the snapshots they produce total
// 6,723,596 bytes and decode in 45ms. His launches were spending 6,797 ms of 8,513 ms
// there. A journal grows with every delta ever streamed; a projection is bounded by
// what the chat shows, and that is the difference the cache turns into launch time.
//
// A miss is never an error: the chat is projected exactly as it always was. Nothing
// under chats/ is read for anything but projection and nothing there is written.
func restoreProjections(writers *events.Writers, paths []string) (map[string]projection.Snapshot, string, int, error) {
	cacheDir := writers.ProjectionCacheDir()
	sessions := make(map[string]projection.Snapshot, len(paths))
	cache := projection.NewCache()
	hits := 0
	for _, path := range paths {
		id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if snapshot, ok := projection.LoadCachedSnapshot(cacheDir, path); ok && snapshot.ID != "" {
			snapshot.ID = id
			sessions[id] = snapshot
			hits++
			continue
		}
		snapshot, err := cache.ProjectFile(path, 0)
		if err != nil {
			return nil, cacheDir, hits, fmt.Errorf("load retained chats: %w", err)
		}
		snapshot.ID = id
		snapshot.LogPath = path
		sessions[id] = snapshot
		projected := int64(0)
		if info, statErr := os.Stat(path); statErr == nil {
			projected = info.Size()
		}
		if storeErr := projection.StoreCachedSnapshot(cacheDir, path, snapshot, projected); storeErr != nil {
			// The product starts whether or not it can cache. Saying so once is
			// better than a silent slow launch every time.
			log.Printf("projection cache not written for %s: %v", id, storeErr)
		}
	}
	projection.PruneCachedSnapshots(cacheDir, paths)
	return sessions, cacheDir, hits, nil
}

// restoreRetainedChats restores every retained chat and returns the id floor
// past everything it holds, including ids it re-minted.
func restoreRetainedChats(writers *events.Writers, registry *session.Registry, bus *events.Bus, floor int64) ([]*session.Session, int64, error) {
	paths, err := writers.DurableChatPaths()
	if err != nil {
		return nil, floor, err
	}
	slimRetainedChats(writers, paths)
	hadDurable := len(paths) > 0
	journals := make(map[string]bool, len(paths))
	for _, path := range paths {
		journals[strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))] = true
	}
	cleanup, err := chatstore.New(writers.ChatDir()).Cleanup(journals)
	if err != nil {
		return nil, floor, err
	}
	registry.ReserveChatIDs(cleanup.IDs)
	log.Printf("startup chat cleanup: removed %d leftover folder(s)", cleanup.Removed)
	for _, path := range cleanup.Kept {
		relative, _ := filepath.Rel(writers.ChatDir(), path)
		log.Printf("startup chat cleanup kept %s: contains files not written by Agent_b", relative)
	}
	if !hadDurable {
		paths, err = writers.LatestOperationalSessionPaths()
		if err != nil {
			return nil, floor, err
		}
	}
	if len(paths) == 0 {
		return nil, floor, nil
	}
	sessions, _, hits, err := restoreProjections(writers, paths)
	if err != nil {
		return nil, floor, err
	}
	startupTimer.mark("parse and project journals")
	log.Printf("startup projections: %d chat(s), %d read from the projection cache, %d projected from the journal", len(sessions), hits, len(sessions)-hits)
	replay := &projection.Replay{Sessions: sessions}
	ids := make([]string, 0, len(replay.Sessions))
	for id := range replay.Sessions {
		ids = append(ids, id)
	}
	// the floor is taken over EVERY restored chat before the first
	// remint, because a remint in s1 must not collide with an id s9 already holds.
	// It used to be a second full projection of the same journals (retainedIDFloor,
	// now gone); it is a pass over the snapshots this function already has.
	for _, id := range ids {
		floor = snapshotIDFloor(replay.Sessions[id], floor)
	}
	sort.Strings(ids)
	result := make([]*session.Session, 0, len(ids))
	for _, id := range ids {
		encoded, marshalErr := json.Marshal(replay.Sessions[id])
		if marshalErr != nil {
			return nil, floor, marshalErr
		}
		var saved session.Snapshot
		if unmarshalErr := json.Unmarshal(encoded, &saved); unmarshalErr != nil {
			return nil, floor, unmarshalErr
		}
		// A RUN THE PROCESS DIED IN IS CLOSED HERE, and the item
		// asked me to verify this already happened. It did not. The projector
		// takes `run.status` from `run.started` and `run.stopped`, so a journal
		// that stops on a `model.delta` — which is exactly what the user's
		// chat s34 does, run r643 started at 08:09:04 and 6,544 deltas in when
		// the drag killed the process — projects as STILL RUNNING, and stayed
		// that way on every later start. A chat that claims to be thinking
		// forever is worse than one that says it was cut.
		saved.Run = closeRunCutByProcessDeath(saved.Run, replay.Sessions[id].Timeline)
		// anchored by rule 3: results older than the newest
		// user message the journal names — surviving or folded — come back as
		// their elision stubs; the JSONL keeps the bytes.
		saved.Messages = contextmgr.StubResultsBefore(saved.Messages, newestRunUserMessage(replay.Sessions[id].Timeline), config.Defaults("").Tools.ReadFile.DefaultLimit)
		// rule 7: a repeated id gets a new one once, with a journal note.
		var reminted []contextmgr.Remint
		saved.Messages, reminted, floor = contextmgr.RemintDuplicateIDs(saved.Messages, floor)
		item, restoreErr := registry.RestoreWithTranscript(saved, replay.Sessions[id].Chat)
		if restoreErr != nil {
			// one chat that cannot be restored is not a
			// reason to refuse to start. It used to return here and the caller
			// called log.Fatal, so a single retained chat naming an agent the
			// operator had since renamed or removed took the whole application
			// down and every other chat with it. The chat is left on disk
			// untouched — the journal is the record and nothing deletes it —
			// and its loss is said out loud rather than swallowed.
			log.Printf("retained chat %s was not restored and was left on disk: %v", id, restoreErr)
			bus.Publish(events.New(events.SessionRestoreFailed, id, "", map[string]any{"session_id": id, "error": restoreErr.Error()}))
			continue
		}
		if len(reminted) > 0 {
			changes := make([]any, 0, len(reminted))
			for _, change := range reminted {
				changes = append(changes, map[string]any{"index": change.Index, "from": change.From, "to": change.To})
			}
			bus.Publish(events.New(events.MessagesReminted, item.ID, "", map[string]any{"reminted": changes}))
		}
		result = append(result, item)
	}
	startupTimer.mark("register restored sessions")
	return result, floor, nil
}

func slimRetainedChats(writers *events.Writers, paths []string) int {
	processed := 0
	for _, path := range paths {
		id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		pending := events.PendingChatPath(writers.ChatDir(), id)
		marker := projection.CacheEntryPath(writers.ProjectionCacheDir(), path) + ".slim"
		if _, markerErr := os.Stat(marker); markerErr == nil {
			if _, pendingErr := os.Stat(pending); os.IsNotExist(pendingErr) {
				continue
			}
		}
		processed++
		changed, err := projection.SlimJournal(path, pending)
		if err != nil {
			log.Printf("startup journal kept %s byte for byte: %v", filepath.Base(path), err)
			continue
		}
		markerErr := os.MkdirAll(filepath.Dir(marker), 0o700)
		if markerErr == nil {
			temporary := marker + ".part"
			if markerErr = os.WriteFile(temporary, []byte("retained-v1\n"), 0o600); markerErr == nil {
				markerErr = tools.AtomicReplace(temporary, marker)
			}
			if markerErr != nil {
				_ = os.Remove(temporary)
			}
		}
		if markerErr != nil {
			log.Printf("startup journal normalization marker not written for %s: %v", filepath.Base(path), markerErr)
		}
		if changed {
			log.Printf("startup journal slimmed %s", filepath.Base(path))
		}
	}
	return processed
}

func retainedSink(writers *events.Writers) func(events.Event) (events.LogCursor, error) {
	reducer := projection.NewRetainedReducer()
	return func(event events.Event) (events.LogCursor, error) {
		return writers.WriteRetainedRecord(event, reducer.Prepare(event))
	}
}

func snapshotIDFloor(snapshot projection.Snapshot, floor int64) int64 {
	note := func(id string) {
		end, start := len(id), len(id)
		for start > 0 && id[start-1] >= '0' && id[start-1] <= '9' {
			start--
		}
		if start == end {
			return
		}
		if value, err := strconv.ParseInt(id[start:end], 10, 64); err == nil && value > floor {
			floor = value
		}
	}
	for _, message := range snapshot.Messages {
		note(message.ID)
	}
	for _, entry := range snapshot.Chat {
		note(entry.RunID)
	}
	for _, event := range snapshot.Timeline {
		note(event.RunID)
	}
	return floor
}

// newestRunUserMessage is the user message id of the newest run.started in a
// chat's journal, or "" when it has none.
func newestRunUserMessage(timeline []events.Event) string {
	for index := len(timeline) - 1; index >= 0; index-- {
		if timeline[index].Type != events.RunStarted {
			continue
		}
		if data, ok := timeline[index].Data.(map[string]any); ok {
			if id, ok := data["user_message_id"].(string); ok && id != "" {
				return id
			}
		}
	}
	return ""
}

func publishPendingSigning(dataRoot string, registry *session.Registry, bus *events.Bus) {
	path := filepath.Join(dataRoot, "signing-applied.pending.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		log.Printf("read pending signing event: %v", err)
		return
	}
	var detail map[string]any
	if err := json.Unmarshal(data, &detail); err != nil {
		log.Printf("decode pending signing event: %v", err)
		return
	}
	bus.Publish(events.New(events.SigningApplied, "", "", detail))
	for _, item := range registry.List() {
		bus.Publish(events.New(events.SigningApplied, item.ID, "", detail))
	}
	if err := os.Remove(path); err != nil {
		log.Printf("remove pending signing event: %v", err)
	}
}

type startupPaths struct {
	Application string
	Data        string
	Workspace   string
	Config      string
}

func resolveStartupPaths(configOverride, applicationOverride, dataOverride string) (startupPaths, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return startupPaths{}, fmt.Errorf("resolve working directory: %w", err)
	}
	// the binary's OWN files - web, prompts, scripts, the
	// example config - live beside the binary, not beside whatever directory a
	// shell happened to be in. Resolving them against the working directory
	// meant the exe started from C:\ or from Explorer looked for its own assets
	// in C:\web and found nothing; the launcher worked only because it set the
	// working directory first. The data root and the workspace are unchanged:
	// those are the user's and are config-driven.
	application := applicationOverride
	if application == "" {
		if executable, execErr := os.Executable(); execErr == nil {
			if resolved, linkErr := filepath.EvalSymlinks(executable); linkErr == nil {
				executable = resolved
			}
			application = filepath.Dir(executable)
		} else {
			application = cwd
		}
	}
	application, err = filepath.Abs(application)
	if err != nil {
		return startupPaths{}, fmt.Errorf("resolve application root: %w", err)
	}

	localData := ""
	if base := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); base != "" {
		localData = filepath.Join(base, "Agent_b")
	}
	configPath := strings.TrimSpace(configOverride)
	data := strings.TrimSpace(dataOverride)
	if configPath == "" {
		configPath = strings.TrimSpace(os.Getenv("AGENTB_CONFIG"))
	}
	if configPath != "" && data == "" {
		if localData != "" {
			data = localData
		} else {
			data = cwd
		}
	}
	if configPath == "" && localData != "" {
		candidate := filepath.Join(localData, "harness.json")
		if _, statErr := os.Stat(candidate); statErr == nil {
			configPath = candidate
			if data == "" {
				data = localData
			}
		} else if !os.IsNotExist(statErr) {
			return startupPaths{}, fmt.Errorf("inspect installed configuration: %w", statErr)
		}
	}
	if configPath == "" {
		configPath = filepath.Join(cwd, "harness.json")
		if data == "" {
			data = cwd
		}
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return startupPaths{}, fmt.Errorf("resolve configuration path: %w", err)
	}
	data, err = filepath.Abs(data)
	if err != nil {
		return startupPaths{}, fmt.Errorf("resolve data root: %w", err)
	}
	return startupPaths{Application: filepath.Clean(application), Data: filepath.Clean(data), Config: filepath.Clean(configPath)}, nil
}

func startupElevationError(elevated bool) error {
	if !elevated {
		return nil
	}
	return fmt.Errorf("SECURITY: Agent_b refuses to run with an elevated Administrator token; local administrators can launch it normally by double-clicking start-Agent_b.cmd in File Explorer (do not use Run as administrator)")
}

// serve binds, then records the process lifetime (when life is non-nil) from
// the moment the listener exists until the server stops, whatever stops it.
func serve(cfg *config.Config, handler http.Handler, life *lifetime, applicationRoot, browserBootstrap string) error {
	httpServer := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	stopped := func(string) {}
	closeRequests := make(chan string, 1)
	if life != nil {
		life.applicationRoot = applicationRoot
		life.listen = cfg.Listen
		life.begin()
		setActiveLifetime(life)
		defer setActiveLifetime(nil)
		if startupApp != nil {
			startupApp.NoteLifecycleStart(life.previousExit)
			life.onExit = func(cause string, uptime int64) {
				startupApp.NoteLifecycleExit(cause, uptime)
				startupApp.SendNow()
			}
		}
		watchSessionEnd(applicationRoot, life.stopped, func() {
			select {
			case closeRequests <- "installer":
			default:
			}
		})
		stopped = life.stopped
	}
	startHostWindow(cfg.Listen, applicationRoot, browserBootstrap, closeRequests)
	errors := make(chan error, 1)
	go func() {
		log.Printf("Agent_b listening on http://%s", cfg.Listen)
		startupTimer.report()
		if startupApp != nil && startupTimer != nil {
			exit := "clean"
			if previousCrashed {
				exit = "crash"
			} else if life != nil && life.unclean {
				exit = "unclean"
			}
			startupApp.NoteListening(time.Since(startupTimer.started).Milliseconds(), exit)
		}
		errors <- httpServer.Serve(listener)
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-signals:
		log.Printf("stopping on %s", sig)
		stopped(fmt.Sprintf("signal %s", sig))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(ctx)
	case cause := <-closeRequests:
		log.Printf("stopping on a close request")
		if life != nil {
			detail := "asked to close"
			if cause == "installer" {
				detail = "asked to close (the installer's graceful stop)"
			}
			life.stoppedCause(cause, detail)
		} else {
			stopped("asked to close")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(ctx)
	case err := <-errors:
		if err != nil && err != http.ErrServerClosed {
			stopped(fmt.Sprintf("the HTTP server failed: %v", err))
			return err
		}
		stopped("the HTTP server closed")
		return nil
	}
}

type servingFacts struct {
	TokenizeIdleMS       int
	TokenizeBusyMS       int
	TokenizeBlocksOnSlot string
	Complete             bool
}

// readServingFacts reads only the accounting facts needed at startup. Missing or
// malformed values remain zero-valued so a documentation issue cannot stop Agent_b.
func readServingFacts(path string) servingFacts {
	file, err := os.Open(path)
	if err != nil {
		return servingFacts{}
	}
	defer file.Close()

	var facts servingFacts
	var idleFound, busyFound, blocksFound bool
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !found {
			continue
		}
		switch key {
		case "tokenize_idle_ms":
			facts.TokenizeIdleMS, err = strconv.Atoi(strings.TrimSpace(value))
			idleFound = err == nil
		case "tokenize_busy_ms":
			facts.TokenizeBusyMS, err = strconv.Atoi(strings.TrimSpace(value))
			busyFound = err == nil
		case "tokenize_blocks_on_slot":
			facts.TokenizeBlocksOnSlot = strings.ToLower(strings.TrimSpace(value))
			blocksFound = facts.TokenizeBlocksOnSlot == "yes" || facts.TokenizeBlocksOnSlot == "no" || facts.TokenizeBlocksOnSlot == "partial"
		}
	}
	facts.Complete = scanner.Err() == nil && idleFound && busyFound && blocksFound
	return facts
}

// the host window, started beside the server in the same
// process. It runs on its own OS thread because a window and its message pump
// belong to one thread, and closing it asks for the same graceful stop the
// installer's channel uses, so every exit still records its reason.
//
// A missing or unusable WebView2 runtime is NOT a failure: the server keeps
// serving, the reason is logged, and the browser window remains the way in.
//
// the window's fate is written to the startup log AND launcher.log
// for every launch, and the window is supervised rather than attempted once.
// Five mornings the sign-in start (-Detached -NoBrowser, so no -window) left a
// server with no window and, because only a window owned the activation event,
// nothing a later launch could signal: the Start menu handoff failed silently.
// The server now owns that event for its whole life; a request with no window
// creates one, and a failed creation is retried with a bounded backoff.
var (
	hostWindowMode         bool
	hostWindowUserData     string
	hostWindowDataRoot     string
	hostWindowRunner       = runHostWindow
	hostWindowRetryDelays  = []time.Duration{time.Second, 3 * time.Second}
	hostWindowAvailability = hostWindowAvailable
)

func startHostWindow(listen, applicationRoot, browserBootstrap string, closeRequests chan string) {
	requests := make(chan struct{}, 1)
	watchActivation(applicationRoot, func() {
		if raiseHostWindow() {
			return
		}
		select {
		case requests <- struct{}{}:
		default:
		}
	})
	url := "http://" + listen + "/chat#agentb-bootstrap=" + browserBootstrap
	go superviseHostWindow(url, applicationRoot, hostWindowMode, requests, closeRequests)
}

func superviseHostWindow(url, applicationRoot string, wanted bool, requests <-chan struct{}, closeRequests chan<- string) {
	if !wanted {
		recordWindowFate("host window: not requested by this launch (a background start); the next launch opens it")
		<-requests
	}
	for {
		if openHostWindow(url, applicationRoot) {
			log.Printf("host window: closed")
			select {
			case closeRequests <- "user":
			default:
			}
			return
		}
		<-requests
	}
}

// openHostWindow reports whether a window opened and was then closed by the
// operator. Every other outcome is recorded and leaves the server serving.
func openHostWindow(url, applicationRoot string) bool {
	if version, err := hostWindowAvailability(); err != nil {
		recordWindowFate(fmt.Sprintf("host window: unavailable, using the browser instead (%v)", err))
		return false
	} else {
		log.Printf("host window: WebView2 runtime %s", version)
	}
	for attempt := 1; ; attempt++ {
		err := hostWindowRunner(url, hostWindowUserData, "Agent_b", applicationRoot)
		if err == nil {
			return true
		}
		if attempt > len(hostWindowRetryDelays) {
			recordWindowFate(fmt.Sprintf("host window: could not open after %d attempts, using the browser instead (%v); the next launch tries again", attempt, err))
			return false
		}
		delay := hostWindowRetryDelays[attempt-1]
		recordWindowFate(fmt.Sprintf("host window: attempt %d failed (%v); retrying in %s", attempt, err, delay))
		time.Sleep(delay)
	}
}

// recordWindowFate writes one window line to the startup log and to the
// launcher log the user reads, in the launcher's own line format.
// the app aggregator, and whether the last instance crashed;
// package level because the listen and window lines live outside main().
var (
	startupApp      *recorder.App
	previousCrashed bool
)

func recordWindowFate(message string) {
	log.Print(message)
	if message == "host window: opened" && startupApp != nil {
		startupApp.NoteWindowShown()
	}
	if hostWindowDataRoot == "" {
		return
	}
	path := filepath.Join(hostWindowDataRoot, "logs", "launcher.log")
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	if file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		_, _ = fmt.Fprintf(file, "%s %s\r\n", time.Now().Format("2006-01-02 15:04:05 -07:00"), printable(message))
		file.Close()
	}
}
