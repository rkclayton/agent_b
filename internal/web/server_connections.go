package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"harness/internal/config"
	"harness/internal/events"
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
		if sessionID, used := s.registry.ConnectionInUse(tail); used {
			writeError(w, 409, "connection in use by session "+sessionID, "connection_id")
			return
		}
		s.mu.Lock()
		assigned := false
		for _, agent := range s.cfg.Agents {
			if agent.B == tail || agent.C == tail || agent.D == tail {
				assigned = true
				break
			}
		}
		if assigned {
			s.mu.Unlock()
			writeError(w, 409, "connection is assigned to an agent role", "agents")
			return
		}
		if len(s.cfg.Connections) == 1 {
			s.mu.Unlock()
			writeError(w, 409, "cannot delete the last connection", "connection_id")
			return
		}
		found := false
		kept := s.cfg.Connections[:0]
		for _, connection := range s.cfg.Connections {
			if connection.ID == tail {
				found = true
				continue
			}
			kept = append(kept, connection)
		}
		s.cfg.Connections = kept
		if !found {
			s.mu.Unlock()
			writeError(w, 404, "connection not found", "connection_id")
			return
		}
		err := s.cfg.Save(s.configPath)
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
	if strings.TrimSpace(tested.BaseURL) == "" {
		writeError(w, 400, "base_url is empty", "connections."+id+".base_url")
		return
	}
	discoveryContext, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	discovered, discoverErr := probe.DiscoverEndpoint(discoveryContext, &tested)
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
		model := strings.TrimSpace(updated.Model)
		if model == "" {
			model = "model"
		}
		modelErr := (&probe.ModelNotListedError{Model: model, Models: discovered.Models}).OperatorMessage()
		writeJSON(w, http.StatusOK, map[string]any{"status": "model_required", "connection_id": id, "base_url": discovered.BaseURL, "models": discovered.Models, "message": "found " + discovered.BaseURL, "error": modelErr})
		return
	}
	// A ready connection that answers exactly as entered is a read-only test.
	// In particular, do not rewrite a display-name model to llama-server's GGUF
	// path and do not alter ProbedAt (which would change the config hash).
	if connection.Capabilities.ProbedAt != "" && tested.BaseURL == connection.BaseURL && tested.Model == connection.Model {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "connection_id": id, "base_url": discovered.BaseURL, "models": discovered.Models, "message": "Test passed"})
		return
	}
	s.startProbe(&updated)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "probing", "connection_id": id, "base_url": discovered.BaseURL, "models": discovered.Models, "message": "found " + discovered.BaseURL})
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
	saveErr := s.cfg.Save(s.configPath)
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
		workspaceRoot, err := filepath.Abs(next.Workspace)
		if err != nil {
			s.mu.Unlock()
			writeError(w, 400, err.Error(), "workspace")
			return
		}
		if err := next.Save(s.configPath); err != nil {
			s.mu.Unlock()
			writeError(w, 500, err.Error(), "config")
			return
		}
		s.cfg = &next
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
