package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/probe"
	"harness/internal/tools"
)

func (s *Server) shellCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.accountMu.TryLock() {
		writeError(w, http.StatusConflict, "service-account setup or credential update is already in progress", "shell.service_account")
		return
	}
	defer s.accountMu.Unlock()
	if s.credential == nil || s.shell == nil {
		writeError(w, http.StatusConflict, "shell credential runtime is unavailable", "shell.service_account")
		return
	}
	var body struct {
		Action   string `json:"action"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	switch body.Action {
	case "store":
		if body.Password == "" {
			writeError(w, http.StatusBadRequest, "password is required", "shell.service_account.password")
			return
		}
		password := []byte(body.Password)
		body.Password = ""
		err := s.credential.Write(password)
		for index := range password {
			password[index] = 0
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "shell.service_account.password")
			return
		}
		status := s.credential.Status()
		s.bus.Publish(events.New(events.ShellCredential, "", "", status))
		writeJSON(w, http.StatusOK, status)
	case "clear":
		if err := s.credential.Clear(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "shell.service_account.password")
			return
		}
		status := s.credential.Status()
		s.bus.Publish(events.New(events.ShellCredential, "", "", status))
		writeJSON(w, http.StatusOK, status)
	case "test":
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		message, err := s.shell.TestServiceAccount(ctx)
		if err != nil {
			writeError(w, http.StatusBadRequest, message, "shell.service_account")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message, "credential": s.credential.Status(), "identity": s.shell.IdentityStatus()})
	default:
		writeError(w, http.StatusBadRequest, "action must be store, test, or clear", "action")
	}
}

func servingFacts(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	wanted := map[string]bool{"tokenize_idle_ms": true, "tokenize_busy_ms": true, "tokenize_blocks_on_slot": true}
	out := map[string]any{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if found && wanted[key] {
			out[key] = strings.TrimSpace(value)
		}
	}
	return out
}
func (s *Server) connections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	s.mu.RLock()
	masked := s.cfg.Masked()
	s.mu.RUnlock()
	writeJSON(w, 200, masked.Connections)
}
func (s *Server) connection(w http.ResponseWriter, r *http.Request) {
	tail := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/connections/"), "/")
	if r.Method == http.MethodDelete && !strings.Contains(tail, "/") {
		// Item 2nc (b): THE REASON SAYS WHAT TO DO. "in use by session s12" named an id
		// the operator has never seen; this names the chat as he knows it and says what to
		// do about it.
		if sessionID, label, used := s.registry.ConnectionHolder(tail); used {
			named := sessionID
			if strings.TrimSpace(label) != "" {
				named = label + " (" + sessionID + ")"
			}
			writeError(w, 409, "in use by the chat "+named+" — close that chat first, then remove this connection", "connections."+tail)
			return
		}
		s.mu.Lock()
		// Item 2mb (b): NAME THE ROLE. "assigned to an agent role" is true and
		// useless; the operator's next move has to be readable off the message.
		// (c): the field is the ROW, so the refusal lands where the click was
		// rather than on a page nobody aimed at.
		if holder := agentRoleHolding(s.cfg.Agents, tail); holder != "" {
			s.mu.Unlock()
			writeError(w, 409, "assigned to "+holder+" — change that assignment on the Agents page, then remove this connection", "connections."+tail)
			return
		}
		if len(s.cfg.Connections) == 1 {
			s.mu.Unlock()
			writeError(w, 409, "this is the only connection — add another one first, then remove this", "connections."+tail)
			return
		}
		// Item 2mb (d): A FRESH SLICE, AND NOTHING ASSIGNED UNTIL THE ID EXISTS.
		//
		// This read s.cfg.Connections[:0] and appended into the LIVE array, so
		// deleting B from [A,B,C] overwrote index 1 in place, and anything still
		// holding the three-element slice — a snapshot, a Masked() copy, a
		// subscriber mid-render — read [A,C,C]. The assignment also happened
		// BEFORE the not-found check, so a delete of an id that does not exist
		// wrote the configuration it was supposed to leave alone.
		//
		// rel-1.23.0/W0 read this rather than reproducing it on the operator's
		// machine, and found it is NOT what turned his page red: the client
		// already keys the message per row. It is a correctness fix and the
		// report says so.
		found := false
		kept := make([]config.Connection, 0, len(s.cfg.Connections))
		for _, connection := range s.cfg.Connections {
			if connection.ID == tail {
				found = true
				continue
			}
			kept = append(kept, connection)
		}
		if !found {
			s.mu.Unlock()
			writeError(w, 404, "connection not found", "connections."+tail)
			return
		}
		s.cfg.Connections = kept
		err := s.saveMachineConfig(*s.cfg)
		masked := s.cfg.Masked()
		s.mu.Unlock()
		if err != nil {
			writeError(w, 500, err.Error(), "config")
			return
		}
		s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": masked}))
		writeJSON(w, 200, map[string]string{"connection_id": tail})
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(tail, "/models") {
		s.queryConnectionModels(w, r, strings.TrimSuffix(tail, "/models"))
		return
	}
	if r.Method != http.MethodPost || !strings.HasSuffix(tail, "/probe") {
		method(w)
		return
	}
	id := strings.TrimSuffix(tail, "/probe")
	connection, ok := s.Connection(id)
	if !ok {
		writeError(w, 404, "connection not found", "connection_id")
		return
	}
	var body struct {
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		// Item 2l1 (a4): the one action uses what is on screen. A freshly typed
		// address, key or timeout is used without a save first, and nothing typed
		// is cleared by running it.
		APIKey          string `json:"api_key"`
		RequestTimeoutS int    `json:"request_timeout_s"`
	}
	if r.Body != nil && r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	tested := *connection
	if strings.TrimSpace(body.BaseURL) != "" {
		tested.BaseURL = strings.TrimSpace(body.BaseURL)
	}
	if strings.TrimSpace(body.Model) != "" {
		tested.Model = strings.TrimSpace(body.Model)
	}
	if strings.TrimSpace(body.APIKey) != "" {
		tested.APIKey = body.APIKey
	}
	if body.RequestTimeoutS > 0 {
		tested.RequestTimeoutS = body.RequestTimeoutS
	}
	if strings.TrimSpace(tested.BaseURL) == "" {
		writeError(w, 400, "base_url is empty", "connections."+id+".base_url")
		return
	}
	discoveryContext, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	// Item 2nb (b): each address is published as it lands, so the sheet says what is
	// being tried and what it found instead of sitting idle for the length of the walk.
	discovered, discoverErr := probe.DiscoverEndpointReporting(discoveryContext, &tested, func(attempt probe.DiscoveryAttempt, tried, total int) {
		s.bus.Publish(events.New(events.ConnectionDiscovering, "", "", map[string]any{
			"connection_id": id,
			"base_url":      attempt.BaseURL,
			"result":        attempt.Result,
			"answered":      attempt.Answered,
			"tried":         tried,
			"total":         total,
		}))
	})
	for _, attempt := range discovered.Attempts {
		s.bus.Publish(events.New(events.ProbeRequest, "", "", map[string]any{
			"connection_id": id, "base_url": attempt.BaseURL, "guard": "operator_typed_host_only", "allowed": attempt.Allowed, "result": attempt.Result,
		}))
	}
	if discoverErr != nil {
		message := discoverErr.Error()
		if friendly, ok := discoverErr.(interface{ OperatorMessage() string }); ok {
			message = friendly.OperatorMessage()
		}
		writeError(w, http.StatusBadRequest, message, "connections."+id+".base_url")
		return
	}
	updated := tested
	updated.BaseURL = discovered.BaseURL
	listed := modelListed(updated.Model, discovered.Models)
	changes := map[string]any{}
	if strings.TrimRight(tested.BaseURL, "/") != strings.TrimRight(discovered.BaseURL, "/") {
		changes["base_url"] = discovered.BaseURL
		writeJSON(w, http.StatusOK, map[string]any{"status": "changes_required", "connection_id": id, "base_url": discovered.BaseURL, "models": discovered.Models, "changes": changes, "message": fmt.Sprintf("changed base_url from %s to %s", tested.BaseURL, discovered.BaseURL)})
		return
	}
	if !listed {
		// Item 2l1 (b),(c),(d): the refusal names the field, the value and what is
		// wanted, and a server that answered with an empty list says THAT rather
		// than blaming the model -- the operator acme case, where Ollama answered
		// 200 with no models pulled.
		modelErr := modelRefusalMessage(updated.Model, discovered.BaseURL, discovered.Models)
		writeJSON(w, http.StatusOK, map[string]any{"status": "model_required", "connection_id": id, "base_url": discovered.BaseURL, "models": discovered.Models, "message": "found " + discovered.BaseURL, "error": modelErr, "proposed": s.proposedConnectionValues(r.Context(), &tested, discovered.Models)})
		return
	}
	// A ready connection that answers exactly as entered is a read-only test.
	// In particular, do not rewrite a display-name model to llama-server's GGUF
	// path and do not alter ProbedAt (which would change the config hash).
	if connection.Capabilities.ProbedAt != "" && tested.BaseURL == connection.BaseURL && tested.Model == connection.Model {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "connection_id": id, "base_url": discovered.BaseURL, "models": discovered.Models, "message": "Test passed", "proposed": s.proposedConnectionValues(r.Context(), &tested, discovered.Models)})
		return
	}
	s.startProbe(&updated)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "probing", "connection_id": id, "base_url": discovered.BaseURL, "models": discovered.Models, "message": "found " + discovered.BaseURL, "proposed": s.proposedConnectionValues(r.Context(), &tested, discovered.Models)})
}

func (s *Server) queryConnectionModels(w http.ResponseWriter, r *http.Request, id string) {
	connection, ok := s.Connection(id)
	if !ok {
		connection = &config.Connection{ID: id, RequestTimeoutS: 30}
	}
	var body struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if !decode(w, r, &body) {
		return
	}
	tested := *connection
	tested.BaseURL = strings.TrimSpace(body.BaseURL)
	if tested.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "Enter base_url before querying models.", "connections."+id+".base_url")
		return
	}
	if body.APIKey != "" {
		tested.APIKey = body.APIKey
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(max(5, tested.RequestTimeoutS))*time.Second)
	defer cancel()
	models, err := llm.New(&tested).Models(ctx)
	queried := strings.TrimRight(tested.BaseURL, "/")
	if strings.HasSuffix(strings.ToLower(queried), "/v1") {
		queried += "/models"
	} else {
		queried += "/v1/models"
	}
	keyState := "no key sent"
	if tested.APIKey != "" {
		keyState = "key sent"
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Query models failed for %s: %v (%s).", queried, err, keyState), "connections."+id+".model")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "url": queried, "key_sent": tested.APIKey != "", "message": fmt.Sprintf("%d model(s) from %s (%s)", len(models), queried, keyState)})
}

func modelListed(configured string, listed []string) bool {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return false
	}
	if len(listed) == 1 {
		return true
	}
	wantBase := strings.TrimSuffix(strings.ToLower(filepath.Base(strings.ReplaceAll(configured, "\\", "/"))), filepath.Ext(configured))
	for _, candidate := range listed {
		if strings.EqualFold(strings.TrimSpace(candidate), configured) {
			return true
		}
		base := filepath.Base(strings.ReplaceAll(strings.TrimSpace(candidate), "\\", "/"))
		stem := strings.TrimSuffix(strings.ToLower(base), filepath.Ext(base))
		if strings.EqualFold(base, filepath.Base(strings.ReplaceAll(configured, "\\", "/"))) || stem == wantBase {
			return true
		}
	}
	return false
}
func (s *Server) startProbe(connection *config.Connection) {
	s.cancelScheduledReachabilityProbe(connection.ID)
	s.probeMu.Lock()
	if prior := s.probeCancels[connection.ID]; prior != nil {
		prior.cancel()
	}
	probeContext, cancel := context.WithCancel(context.Background())
	current := &probeRun{cancel: cancel}
	s.probeCancels[connection.ID] = current
	s.probeMu.Unlock()
	go s.runProbe(probeContext, connection, current)
}
func (s *Server) runProbe(ctx context.Context, connection *config.Connection, current *probeRun) {
	caps, findings, err := probe.Probe(ctx, connection)
	probeSucceeded := err == nil
	wasCurrent := s.clearProbe(connection.ID, current)
	if ctx.Err() != nil {
		if wasCurrent {
			s.completeReachabilityProbe(connection.ID, false)
		}
		return
	}
	s.completeReachabilityProbe(connection.ID, probeSucceeded)
	// Item 2gy: only a clear NO downgrades a stored finding. A probe that could
	// not reach a conclusion - a timeout, a busy slot, an unreachable server -
	// keeps what was known, says since when nobody has confirmed it, and tries
	// again on a backoff. A model that could not answer in time is not a model
	// that cannot call tools.
	outcome := classifyProbe(err)
	if outcome == probeInconclusive {
		caps = connection.Capabilities
		findings = keepFindingsUnverified(connection.Capabilities.Findings, time.Now(), err.Error())
		s.scheduleProbeRetry(connection.ID)
	} else {
		s.resetProbeRetries(connection.ID)
		if err != nil {
			caps, findings = failedProbeCapabilities(connection, err)
		}
	}
	s.mu.Lock()
	// Item 2gb: the probe's findings name the shell that backs the shell tool
	// on this host, so the operator can see which dialect the model is told to
	// write. It is a host fact, not the server's, and is recorded either way.
	findings = append(findings, tools.ShellHostFinding(s.cfg.Shell))
	caps.Findings = findings
	for i := range s.cfg.Connections {
		if s.cfg.Connections[i].ID == connection.ID {
			s.cfg.Connections[i].Capabilities = caps
			s.cfg.Connections[i].Reasoning.ValidEfforts = append([]string(nil), caps.ValidEfforts...)
			if s.cfg.Connections[i].Context.NCtx == 0 {
				s.cfg.Connections[i].Context.NCtx = caps.NCtx
			}
		}
	}
	saveErr := s.saveMachineConfig(*s.cfg)
	s.mu.Unlock()
	if saveErr != nil {
		s.bus.Publish(events.New(events.Error, "", "", map[string]any{"where": "config", "message": saveErr.Error()}))
		return
	}
	if s.registry != nil {
		s.registry.RefreshRunnable()
	}
	s.bus.Publish(events.New(events.ConnectionProbed, "", "", map[string]any{"connection_id": connection.ID, "capabilities": caps, "findings": findings, "outcome": outcome.String()}))
	if probeSucceeded && s.scheduler != nil {
		s.scheduler.ReleaseModel(connection.ID)
	}
}

func (s *Server) clearProbe(connectionID string, current *probeRun) bool {
	s.probeMu.Lock()
	cleared := false
	if s.probeCancels[connectionID] == current {
		delete(s.probeCancels, connectionID)
		cleared = true
	}
	s.probeMu.Unlock()
	return cleared
}

func failedProbeCapabilities(connection *config.Connection, err error) (config.Capabilities, []string) {
	caps := connection.Capabilities
	message := err.Error()
	detail := ""
	if friendly, ok := err.(interface {
		OperatorMessage() string
		Diagnostic() string
	}); ok {
		message, detail = friendly.OperatorMessage(), friendly.Diagnostic()
	}
	findings := []string{"probe failed: " + message}
	if detail != "" {
		findings = append(findings, "probe detail: "+detail)
	}
	caps.Findings = findings
	return caps, findings
}

// contextBackground implements the small Context surface while avoiding probe cancellation after the request returns.
type contextBackground struct{}

func (contextBackground) Deadline() (time.Time, bool) { return time.Time{}, false }
func (contextBackground) Done() <-chan struct{}       { return nil }
func (contextBackground) Err() error                  { return nil }
func (contextBackground) Value(any) any               { return nil }

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, s.ConfigSnapshot().Masked())
	case http.MethodPost:
		var patch map[string]any
		if !decode(w, r, &patch) {
			return
		}
		if !s.requireOperatorConfigRequest(w, r, patch) {
			return
		}
		if field := directNetworkPolicyField(patch); field != "" {
			writeError(w, http.StatusBadRequest, "apply the LAN policy through Settings > Security so Windows policy is verified before configuration is saved", field)
			return
		}
		if s.applyOperatorContextPatch(w, r, patch) {
			return
		}
		// Item 2nb (f): NO VALUE CARRYING THE MASK SENTINEL REACHES THE CONFIGURATION.
		// The merge used to DELETE an api_key that equalled the sentinel exactly, which
		// is a silent drop: a value that merely contained it, or arrived by another path,
		// would have been stored as the operator's key. A refusal says what happened.
		if field, found := patchCarriesMaskedKey(patch); found {
			writeError(w, http.StatusBadRequest, "the API key field still holds the placeholder for a stored key; type the key to replace it, or leave it alone", field)
			return
		}
		s.mu.Lock()
		previousConnections := make(map[string]config.Connection, len(s.cfg.Connections))
		for _, connection := range s.cfg.Connections {
			previousConnections[connection.ID] = connection
		}
		currentBytes, _ := json.Marshal(s.cfg)
		var current map[string]any
		_ = json.Unmarshal(currentBytes, &current)
		mergeConfig(current, patch)
		merged, _ := json.Marshal(current)
		var next config.Config
		if err := json.Unmarshal(merged, &next); err != nil {
			s.mu.Unlock()
			writeError(w, 400, err.Error(), "config")
			return
		}
		config.ApplyDefaults(&next)
		if err := next.Validate(); err != nil {
			s.mu.Unlock()
			writeError(w, 400, err.Error(), configField(err, next))
			return
		}
		if err := config.ResolveConnectionCredentials(&next, s.roots.Data); err != nil {
			s.mu.Unlock()
			writeError(w, 400, err.Error(), configField(err, next))
			return
		}
		workspacePath := next.Workspace
		if !filepath.IsAbs(workspacePath) {
			workspacePath = filepath.Join(s.roots.Profile, workspacePath)
		}
		workspaceRoot, err := filepath.Abs(workspacePath)
		if err != nil {
			s.mu.Unlock()
			writeError(w, 400, err.Error(), "workspace")
			return
		}
		previous := *s.cfg
		// Item 2jg (d): turning the switch on issues a NEW install id, so two runs
		// of telemetry from one machine cannot be joined. It is reissued here,
		// before the save, so the new id is what lands on disk.
		if next.Telemetry.Enabled && (!previous.Telemetry.Enabled || next.Telemetry.InstallID == "") {
			next.Telemetry.InstallID = NewInstallID()
		}
		*s.cfg = next
		if s.profiles != nil {
			if err := s.profiles.SaveActive(); err != nil {
				*s.cfg = previous
				s.mu.Unlock()
				writeError(w, 500, err.Error(), "profiles")
				return
			}
		}
		if err := s.saveMachineConfig(next); err != nil {
			*s.cfg = previous
			if s.profiles != nil {
				_ = s.profiles.SaveActive()
			}
			s.mu.Unlock()
			writeError(w, 500, err.Error(), "config")
			return
		}
		s.roots.Workspace = filepath.Clean(workspaceRoot)
		masked := next.Masked()
		var reprobe []config.Connection
		for _, connection := range next.Connections {
			before, existed := previousConnections[connection.ID]
			if existed && (before.BaseURL != connection.BaseURL || before.Model != connection.Model) {
				reprobe = append(reprobe, connection)
			}
		}
		s.mu.Unlock()
		for index := range reprobe {
			s.startProbe(&reprobe[index])
		}
		if s.runner != nil {
			s.runner.Configure(s.ConfigSnapshot())
		}
		// Item 2jg (d): the switch does not filter, it detaches. Applying the
		// configuration is the only place collection starts or stops, so there is
		// one answer to "is anything collecting" rather than two.
		s.applyTelemetry(s.ConfigSnapshot())
		if s.registry != nil {
			s.registry.RefreshRunnable()
		}
		if s.prompt != nil {
			if err := s.prompt.Reload(); err != nil {
				writeError(w, 500, err.Error(), "prompts/system.md")
				return
			}
		}
		if s.runner != nil {
			go func() {
				for _, item := range s.registry.List() {
					s.runner.PublishBudget(contextBackground{}, item)
				}
			}()
		}
		s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": masked}))
		if s.updater != nil {
			s.updater.ConfigChanged(context.Background())
		}
		writeJSON(w, 200, masked)
	default:
		method(w)
	}
}

// ApplyConnector is reachable only after the browser-resolved approval gate.
// The public config endpoint retains its browser-session and mutation-token checks.
func (s *Server) ApplyConnector(change tools.ConnectorChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := *s.cfg
	next.Services = make(map[string]config.Service, len(s.cfg.Services)+1)
	for name, service := range s.cfg.Services {
		next.Services[name] = service
	}
	_, exists := next.Services[change.Name]
	switch change.Operation {
	case "add":
		if exists {
			return fmt.Errorf("connector %q already exists", change.Name)
		}
		next.Services[change.Name] = change.Service
	case "edit":
		if !exists {
			return fmt.Errorf("connector %q does not exist", change.Name)
		}
		next.Services[change.Name] = change.Service
	case "remove":
		if !exists {
			return fmt.Errorf("connector %q does not exist", change.Name)
		}
		delete(next.Services, change.Name)
	default:
		return fmt.Errorf("invalid connector operation")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := s.saveMachineConfig(next); err != nil {
		return err
	}
	*s.cfg = next
	go func() {
		if s.runner != nil {
			s.runner.Configure(s.ConfigSnapshot())
		}
		s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": s.ConfigSnapshot().Masked()}))
	}()
	return nil
}

func configField(err error, cfg config.Config) string {
	field := strings.SplitN(err.Error(), ":", 2)[0]
	if !strings.HasPrefix(field, "connections[") {
		return field
	}
	end := strings.Index(field, "]")
	if end < 9 {
		return field
	}
	var index int
	if _, scanErr := fmt.Sscanf(field[:end+1], "connections[%d]", &index); scanErr != nil || index < 0 || index >= len(cfg.Connections) {
		return field
	}
	return "connections." + cfg.Connections[index].ID + field[end+1:]
}

// proposedConnectionValues is what the one action learned, offered as SOFT values
// the operator sees in the fields and can change before saving. Item 2l1 (a5) and
// (a7): the window is read from what the server publishes -- vLLM's max_model_len
// on /v1/models, which the probe already fetches -- and where nothing publishes
// it the field says the value is unverified instead of presenting a guess as a
// measurement. Nothing here is written to configuration.
// proposedLabelFor answers item 2mh (a): the name a connection would have if it were
// named after what it serves. It is empty when the operator has named the connection
// himself, and when there is no model to name it after.
func proposedLabelFor(tested *config.Connection, models []string) string {
	if tested == nil {
		return ""
	}
	label := strings.TrimSpace(tested.Label)
	if label != "" && !strings.EqualFold(label, strings.TrimSpace(tested.ID)) {
		return ""
	}
	model := strings.TrimSpace(tested.Model)
	if model == "" || strings.EqualFold(model, "model") {
		model = strings.TrimSpace(onlyModel(models))
	}
	if model == "" || strings.EqualFold(model, "model") {
		return ""
	}
	// A llama.cpp model is a full GGUF path; the switcher already reduces one to its
	// basename (item 2mh (d)) and the label uses the same reduction rather than a
	// second one.
	model = strings.TrimSuffix(model, ".gguf")
	if index := strings.LastIndexAny(model, `/\`); index >= 0 {
		model = model[index+1:]
	}
	return strings.TrimSpace(model)
}

func (s *Server) proposedConnectionValues(ctx context.Context, tested *config.Connection, models []string) map[string]any {
	proposed := map[string]any{"model_selected": onlyModel(models), "context_source": "unverified"}
	// The catalog read is enrichment, not the answer: it is bounded to a few
	// seconds regardless of the connection request timeout, which defaults to 900,
	// so a server that does not serve this route cannot delay the one action.
	catalogCtx, cancel := context.WithTimeout(ctx, min(time.Duration(max(3, tested.RequestTimeoutS))*time.Second, 8*time.Second))
	defer cancel()
	published := 0
	if entries, err := llm.New(tested).ModelCatalog(catalogCtx); err == nil {
		for _, entry := range entries {
			if entry.ContextLength <= 0 {
				continue
			}
			if strings.TrimSpace(tested.Model) == "" || strings.EqualFold(strings.TrimSpace(entry.ID), strings.TrimSpace(tested.Model)) || len(entries) == 1 {
				published = entry.ContextLength
				break
			}
		}
	}
	window := published
	if window == 0 {
		window = tested.Capabilities.NCtx
		if window > 0 {
			proposed["context_source"] = "probed"
		}
	} else {
		proposed["context_source"] = "published"
	}
	if window > 0 {
		proposed["n_ctx"] = window
		proposed["reserve_output"] = config.ReserveOutputFor(window)
		proposed["reasoning_max_tokens"] = config.ReasoningShareFor(config.ReserveOutputFor(window))
	}
	if efforts := tested.Capabilities.ValidEfforts; len(efforts) > 0 {
		proposed["valid_efforts"] = efforts
		if !slices.Contains(efforts, tested.Reasoning.Effort) {
			proposed["effort"] = efforts[len(efforts)/2]
		}
	}
	proposed["reasoning_enabled"] = tested.Capabilities.ReasoningControl != "" && tested.Capabilities.ReasoningControl != "none"
	// Item 2mh (a): TEST NAMES THE CONNECTION WHEN THE OPERATOR HAS NOT. A connection
	// is born labelled with its own generated id — "server-2" identifies nothing, at
	// any width — and the one thing Test learns that would identify it is the model.
	// It arrives as a PROPOSED value through 2l1's path, which the operator can
	// accept or overwrite; a label he has touched is never replaced, whatever it
	// says, and that rule is enforced here and again in the browser.
	if label := proposedLabelFor(tested, models); label != "" {
		proposed["label"] = label
	}
	return proposed
}

// onlyModel is item 2l1 (a3)'s first clause: a server offering exactly one model
// has that model selected. Anything else is left to the picker, which keeps the
// previous choice when it is still offered and otherwise stays empty.
func onlyModel(models []string) string {
	if len(models) == 1 {
		return strings.TrimSpace(models[0])
	}
	return ""
}

// modelRefusalMessage names the field, the value and what is wanted. Item 2l1 (c)
// and (d): a server that answered with an EMPTY list says that, rather than
// blaming the model name -- the operator acme case, where Ollama answered 200
// with no model pulled, and the old wording accused his model of not being served.
func modelRefusalMessage(model, baseURL string, models []string) string {
	model = strings.TrimSpace(model)
	switch {
	case len(models) == 0 && model == "":
		return "model is empty, and " + baseURL + " answered but listed no models at all. Pull or load a model on that server, or type the model name by hand."
	case len(models) == 0:
		return baseURL + " answered but listed no models at all, so " + strconv.Quote(model) + " cannot be confirmed. Pull or load a model on that server, or keep this name if the server accepts it."
	case model == "":
		return "model is empty — choose one of the " + strconv.Itoa(len(models)) + " models this server listed."
	default:
		return (&probe.ModelNotListedError{Model: model, Models: models}).OperatorMessage()
	}
}

// agentRoleHolding names the first agent role holding a connection, for item
// 2mb (b). "assigned to an agent role" is true and useless; "assigned to Coder's
// B role" is where the operator goes next.
func agentRoleHolding(agents []config.Agent, connectionID string) string {
	var held []string
	for _, agent := range agents {
		var roles []string
		for _, role := range []struct {
			name string
			id   string
		}{{"B", agent.B}, {"C", agent.C}, {"D", agent.D}} {
			if role.id == connectionID {
				roles = append(roles, role.name)
			}
		}
		if len(roles) == 0 {
			continue
		}
		// ALL of them, not the first. A connection can hold two roles on one
		// agent, and clearing the one the message named would leave the delete
		// refused again for a reason the operator thought they had dealt with.
		name := agent.Name
		if name == "" {
			held = append(held, "the "+strings.Join(roles, " and ")+" role")
			continue
		}
		held = append(held, name+"'s "+strings.Join(roles, " and ")+" role")
	}
	if len(held) == 0 {
		return ""
	}
	return strings.Join(held, ", ")
}

// maskedKeySentinel is what a stored key is shown as. It must never travel back.
const maskedKeySentinel = "••••"

// patchCarriesMaskedKey is item 2nb (f)'s guard: it finds any api_key in an incoming
// patch that CONTAINS the mask, at any depth, and names the field so the refusal lands
// on the row the operator was looking at.
func patchCarriesMaskedKey(patch map[string]any) (string, bool) {
	connections, _ := patch["connections"].([]any)
	for _, raw := range connections {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		value, _ := item["api_key"].(string)
		if strings.Contains(value, maskedKeySentinel) {
			id, _ := item["id"].(string)
			if id == "" {
				return "connections.api_key", true
			}
			return "connections." + id + ".api_key", true
		}
	}
	return "", false
}
