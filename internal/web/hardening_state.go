package web

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"

	"harness/internal/buildinfo"
	"harness/internal/events"
	"harness/internal/hardening"
	"harness/internal/signing"
)

type hostProtectionState struct {
	hardening     hardening.Manager
	hardeningMu   sync.RWMutex
	hardeningOp   hardeningOperation
	signing       signing.Manager
	signingMu     sync.Mutex
	signingStatus signing.Status
}

func (s *Server) hardeningManager() hardening.Manager { return s.protection.hardening }

type hardeningOperation struct {
	Action     string `json:"action,omitempty"`
	State      string `json:"state,omitempty"`
	Message    string `json:"message,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type hardeningResponse struct {
	hardening.Status
	Operation hardeningOperation `json:"operation"`
}

func (s *Server) beginHardening(action, message string) {
	s.protection.hardeningMu.Lock()
	s.protection.hardeningOp = hardeningOperation{
		Action: action, State: "running", Message: message,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.protection.hardeningMu.Unlock()
}

func (s *Server) progressHardening(message string) {
	s.protection.hardeningMu.Lock()
	s.protection.hardeningOp.Message = message
	s.protection.hardeningMu.Unlock()
}

func (s *Server) finishHardening(state, message string) {
	s.protection.hardeningMu.Lock()
	s.protection.hardeningOp.State = state
	s.protection.hardeningOp.Message = message
	s.protection.hardeningOp.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	s.protection.hardeningMu.Unlock()
	if state == "failed" && s.bus != nil {
		s.bus.Publish(events.New(events.Error, "", "", map[string]any{"where": "hardening", "message": message}))
	}
}

func (s *Server) hardeningOperation() hardeningOperation {
	s.protection.hardeningMu.RLock()
	defer s.protection.hardeningMu.RUnlock()
	return s.protection.hardeningOp
}

func (s *Server) writeHardeningStatus(w http.ResponseWriter, status hardening.Status) {
	writeJSON(w, http.StatusOK, hardeningResponse{Status: status, Operation: s.hardeningOperation()})
}

// signingState is read-only build identity for About. Release signing is an
// offline publisher operation; the running application exposes no signing API.
func (s *Server) signingState() signing.Status {
	s.protection.signingMu.Lock()
	defer s.protection.signingMu.Unlock()
	return s.protection.signingStatus
}

func (s *Server) RefreshSigningState(ctx context.Context) error {
	status, err := s.protection.signing.Status(ctx, signing.Request{ExpectedHash: buildinfo.Current().ExecutableSHA256, ProcessID: os.Getpid()})
	if err != nil {
		return err
	}
	s.protection.signingMu.Lock()
	s.protection.signingStatus = status
	s.protection.signingMu.Unlock()
	return nil
}
