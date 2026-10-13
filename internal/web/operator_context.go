package web

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"harness/internal/events"
	"harness/internal/tools"
)

type operatorState struct {
	changeMu sync.Mutex
	mu       sync.Mutex
	enabled  bool
	expires  string
	timer    operatorTimer
	epoch    uint64
	request  func(*http.Request) error
	now      func() time.Time
	after    func(time.Duration, func()) operatorTimer
}

func newOperatorState() operatorState {
	return operatorState{
		request: requireOperatorHTTPClient,
		now:     time.Now,
		after: func(duration time.Duration, fn func()) operatorTimer {
			return time.AfterFunc(duration, fn)
		},
	}
}

func (s *Server) operatorOwnedRequest(r *http.Request) error { return s.operatorState.request(r) }
func (s *Server) operatorRequestAvailable() bool             { return s.operatorState.request != nil }
func (s *Server) operatorContextSnapshot() (bool, string) {
	s.operatorState.mu.Lock()
	defer s.operatorState.mu.Unlock()
	return s.operatorState.enabled, s.operatorState.expires
}

func (s *Server) setOperatorContext(enabled bool, reason string, expectedEpoch uint64) tools.ShellIdentityStatus {
	s.operatorState.changeMu.Lock()
	defer s.operatorState.changeMu.Unlock()
	s.mu.RLock()
	timeoutMinutes := s.cfg.Shell.OperatorContextIdleTimeoutMinutes
	s.mu.RUnlock()

	s.operatorState.mu.Lock()
	if expectedEpoch != 0 && expectedEpoch != s.operatorState.epoch {
		status := s.currentOperatorStatusLocked()
		s.operatorState.mu.Unlock()
		return status
	}
	s.resetOperatorTimerLocked(enabled, timeoutMinutes)
	s.operatorState.mu.Unlock()
	return s.applyOperatorContext(enabled, reason)
}

func (s *Server) touchOperatorContext(reason string) {
	s.operatorState.changeMu.Lock()
	defer s.operatorState.changeMu.Unlock()
	s.mu.RLock()
	timeoutMinutes := s.cfg.Shell.OperatorContextIdleTimeoutMinutes
	s.mu.RUnlock()
	s.operatorState.mu.Lock()
	if !s.operatorState.enabled {
		s.operatorState.mu.Unlock()
		return
	}
	s.resetOperatorTimerLocked(true, timeoutMinutes)
	s.operatorState.mu.Unlock()
	s.applyOperatorContext(true, reason)
}

func (s *Server) resetOperatorTimerLocked(enabled bool, timeoutMinutes int) {
	s.operatorState.epoch++
	epoch := s.operatorState.epoch
	if s.operatorState.timer != nil {
		s.operatorState.timer.Stop()
		s.operatorState.timer = nil
	}
	s.operatorState.enabled = enabled
	s.operatorState.expires = ""
	if !enabled {
		return
	}
	duration := time.Duration(timeoutMinutes) * time.Minute
	expires := s.operatorState.now().Add(duration).UTC()
	s.operatorState.expires = expires.Format(time.RFC3339)
	s.operatorState.timer = s.operatorState.after(duration, func() {
		s.setOperatorContext(false, "idle timeout expired", epoch)
	})
}

func (s *Server) applyOperatorContext(enabled bool, reason string) tools.ShellIdentityStatus {
	runtimeConfig := s.ConfigSnapshot()
	if s.runner != nil {
		s.runner.Configure(runtimeConfig)
	} else if s.shell != nil {
		s.shell.Configure(runtimeConfig)
	}
	status := tools.ShellIdentityStatus{}
	if s.shell != nil {
		status = s.shell.IdentityStatus()
	}
	s.publishOperatorContext(enabled, reason, status.OperatorContextExpiresAt)
	if s.bus != nil {
		s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": runtimeConfig.Masked()}))
	}
	return status
}

func (s *Server) currentOperatorStatusLocked() tools.ShellIdentityStatus {
	if !s.operatorState.enabled {
		return tools.ShellIdentityStatus{}
	}
	return tools.ShellIdentityStatus{
		OperatorContext:          true,
		OperatorContextExpiresAt: s.operatorState.expires,
		Reason:                   "Run as you is on; tools are running as the Windows account that launched Agent_b",
	}
}

func (s *Server) publishOperatorContext(enabled bool, reason, expiresAt string) {
	data := map[string]any{"enabled": enabled, "reason": reason, "expires_at": expiresAt}
	log.Printf("SECURITY: operator context enabled=%t reason=%q expires_at=%q", enabled, reason, expiresAt)
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.New(events.OperatorContext, "", "", data))
	if s.registry == nil {
		return
	}
	for _, item := range s.registry.List() {
		s.bus.Publish(events.New(events.OperatorContext, item.ID, "", data))
	}
}

func operatorContextPatch(patch map[string]any) (enabled bool, present bool, only bool, err error) {
	raw, ok := patch["shell"]
	if !ok {
		return false, false, false, nil
	}
	shell, ok := raw.(map[string]any)
	if !ok {
		return false, false, false, nil
	}
	value, present := shell["operator_context"]
	if !present {
		if _, protected := shell["operator_context_idle_timeout_minutes"]; protected {
			return false, false, false, fmt.Errorf("shell.operator_context_idle_timeout_minutes can be changed only in the protected configuration file while Agent_b is stopped")
		}
		return false, false, false, nil
	}
	enabled, ok = value.(bool)
	if !ok {
		return false, true, false, fmt.Errorf("shell.operator_context must be boolean")
	}
	return enabled, true, len(patch) == 1 && len(shell) == 1, nil
}

func (s *Server) applyOperatorContextPatch(w http.ResponseWriter, r *http.Request, patch map[string]any) bool {
	enabled, present, only, err := operatorContextPatch(patch)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error(), "shell.operator_context")
		return true
	}
	if !present {
		return false
	}
	if !only {
		writeError(w, http.StatusBadRequest, "save other settings separately from the Run as you switch", "shell.operator_context")
		return true
	}
	reason := "disabled by user request"
	if enabled {
		reason = "enabled by verified process of the user"
	}
	s.setOperatorContext(enabled, reason, 0)
	writeJSON(w, http.StatusOK, s.ConfigSnapshot().Masked())
	return true
}

func protectedShellConfigField(patch map[string]any) string {
	if raw, ok := patch["operator_files"].(map[string]any); ok {
		if _, present := raw["allow_mailbox_approvals"]; present {
			return "operator_files.allow_mailbox_approvals"
		}
	}
	if rawTools, ok := patch["tools"]; ok {
		if toolConfig, ok := rawTools.(map[string]any); ok {
			if rawShell, ok := toolConfig["shell"]; ok {
				if shellTool, ok := rawShell.(map[string]any); ok {
					if _, present := shellTool["operator_commands"]; present {
						return "tools.shell.operator_commands"
					}
				}
			}
		}
	}
	raw, ok := patch["shell"]
	if !ok {
		return ""
	}
	shell, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	for _, field := range []string{"operator_context", "operator_context_idle_timeout_minutes", "service_account", "allow_local_network", "confirmed_local_subnets", "allowed_model_ranges"} {
		if _, present := shell[field]; present {
			return "shell." + field
		}
	}
	return ""
}

func directNetworkPolicyField(patch map[string]any) string {
	raw, ok := patch["shell"]
	if !ok {
		return ""
	}
	shell, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	for _, field := range []string{"allow_local_network", "confirmed_local_subnets", "allowed_model_ranges", "trusted_folders"} {
		if _, present := shell[field]; present {
			return "shell." + field
		}
	}
	return ""
}

func (s *Server) requireOperatorConfigRequest(w http.ResponseWriter, r *http.Request, patch map[string]any) bool {
	field := protectedShellConfigField(patch)
	if field == "" {
		return true
	}
	if shell, ok := patch["shell"].(map[string]any); ok {
		if service, ok := shell["service_account"].(map[string]any); ok {
			if enabled, present := service["enabled"].(bool); present && enabled {
				writeError(w, http.StatusBadRequest, "turn on the service identity with its Security setup flow so the account and credential are tested first", "shell.service_account.enabled")
				return false
			}
		}
	}
	if err := s.operatorState.request(r); err != nil {
		log.Printf("SECURITY: protected config mutation refused field=%s: %v", field, err)
		writeError(w, http.StatusForbidden, "security-sensitive shell settings refused: "+err.Error(), field)
		return false
	}
	return true
}
