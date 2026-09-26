package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"harness/internal/credential"
	"harness/internal/events"
	"harness/internal/serviceaccount"
	"harness/internal/tools"
)

const managedServiceAccount = credential.ServiceAccount

func (s *Server) serviceAccount(w http.ResponseWriter, r *http.Request) {
	if s.account == nil || s.credential == nil || s.shell == nil {
		writeError(w, http.StatusConflict, "service-account setup runtime is unavailable", "shell.service_account")
		return
	}
	account := managedServiceAccount

	switch r.Method {
	case http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		status, err := s.account.Status(ctx, account)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "shell.service_account")
			return
		}
		s.accountMu.Lock()
		status = s.serviceAccountState(r.Context(), status)
		s.accountMu.Unlock()
		writeJSON(w, http.StatusOK, status)
	case http.MethodPost:
		s.setupServiceAccount(w, r, account)
	default:
		method(w)
	}
}

func (s *Server) setupServiceAccount(w http.ResponseWriter, r *http.Request, account string) {
	if !s.accountMu.TryLock() {
		writeError(w, http.StatusConflict, "another service-account setup is already in progress", "shell.service_account")
		return
	}
	defer s.accountMu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		Action            string   `json:"action"`
		ConnectionID      string   `json:"connection_id"`
		AllowLocalNetwork bool     `json:"allow_local_network"`
		LocalSubnets      []string `json:"local_subnets"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Action != "provision" {
		writeError(w, http.StatusBadRequest, "action must be provision", "action")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	status, err := s.account.Status(ctx, account)
	cancel()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "shell.service_account")
		return
	}
	if !status.Supported {
		writeError(w, http.StatusBadRequest, "local service-account setup is supported only on Windows", "shell.service_account")
		return
	}
	if status.Administrator {
		writeError(w, http.StatusConflict, "refusing to manage an account that belongs to Administrators", "shell.service_account")
		return
	}
	currentState := s.serviceAccountState(r.Context(), status)
	if currentState.State == "ready" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": "service identity already ready: existing agentb-svc credential works; no account change was needed",
			"account": currentState,
		})
		return
	}
	protectionRequest, err := s.hardeningRequest(body.ConnectionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "connection_id")
		return
	}
	protectionRequest.AllowLocalNetwork = body.AllowLocalNetwork
	protectionRequest.LocalSubnets = append([]string(nil), body.LocalSubnets...)

	previous, hadPrevious, err := s.previousCredential()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the existing credential could not be preserved before setup", "shell.service_account.password")
		return
	}
	defer clearSecret(previous)
	password, err := generatedServicePassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "shell.service_account.password")
		return
	}
	if err := s.credential.Write(password); err != nil {
		clearSecret(password)
		writeError(w, http.StatusInternalServerError, err.Error(), "shell.service_account.password")
		return
	}
	clearSecret(password)

	// The request is deliberately detached while Windows displays UAC. Closing
	// the browser must not strand an account operation halfway through.
	setupContext, setupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	result, setupErr := s.account.Setup(setupContext, account, s.credential.Path(), status.Exists, &serviceaccount.Protection{
		ApplicationDirectory: protectionRequest.ApplicationDirectory,
		DataDirectory:        protectionRequest.DataDirectory,
		WorkspaceDirectory:   protectionRequest.WorkspaceDirectory,
		ExchangeDirectory:    protectionRequest.ExchangeDirectory,
		ModelAddress:         protectionRequest.ModelAddress,
		ModelPort:            protectionRequest.ModelPort,
		AllowLocalNetwork:    protectionRequest.AllowLocalNetwork,
		LocalSubnets:         protectionRequest.LocalSubnets,
		AllowedModelRanges:   protectionRequest.AllowedModelRanges,
	})
	setupCancel()
	if setupErr != nil {
		if !result.Attempted {
			if restoreErr := s.restoreCredential(previous, hadPrevious); restoreErr != nil {
				writeError(w, http.StatusInternalServerError, setupErr.Error()+"; restoring the prior credential also failed", "shell.service_account")
				return
			}
			writeError(w, http.StatusBadRequest, setupErr.Error()+"; no account change was attempted", "shell.service_account")
			return
		}
		credentialStatus := s.credential.Status()
		s.bus.Publish(events.New(events.ShellCredential, "", "", credentialStatus))
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":      setupErr.Error() + "; the elevated script started, so inspect the account and use Reset password before continuing",
			"field":      "shell.service_account",
			"credential": credentialStatus,
			"attempted":  true,
		})
		return
	}

	testConfig := s.ConfigSnapshot()
	testConfig.Shell.ServiceAccount.Account = account
	testConfig.Shell.ServiceAccount.Domain = "."
	testConfig.Shell.ServiceAccount.Enabled = true
	s.shell.Configure(testConfig)
	testContext, testCancel := context.WithTimeout(context.Background(), 30*time.Second)
	testMessage, testErr := s.shellTest(testContext)
	testCancel()
	if testErr != nil {
		s.shell.Configure(s.ConfigSnapshot())
		writeError(w, http.StatusBadRequest, "service identity not set up: "+testMessage, "shell.service_account")
		return
	}

	masked, err := s.enableConfiguredServiceAccount(account, body.AllowLocalNetwork, body.LocalSubnets)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "the account and credential were updated, but enabling the identity split failed: " + err.Error(),
			"field": "shell.service_account.enabled",
		})
		return
	}
	credentialStatus := s.credential.Status()
	s.bus.Publish(events.New(events.ShellCredential, "", "", credentialStatus))
	s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": masked}))

	currentStatus := status
	inspectContext, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if inspected, inspectErr := s.account.Status(inspectContext, account); inspectErr == nil {
		currentStatus = inspected
	}
	inspectCancel()
	message := "service identity set up: account, protections, credential test, and identity split are ready"
	if status.Exists {
		message = "service identity repaired: adopted existing agentb-svc by password reset; protections, credential test, and identity split are ready"
	}
	response := map[string]any{
		"ok":         true,
		"message":    message,
		"account":    currentStatus,
		"credential": credentialStatus,
		"identity":   s.shell.IdentityStatus(),
		"config":     masked,
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) disableConfiguredServiceAccount(account string) (any, error) {
	s.mu.Lock()
	previous := *s.cfg
	s.cfg.Shell.ServiceAccount.Account = account
	s.cfg.Shell.ServiceAccount.Domain = "."
	s.cfg.Shell.ServiceAccount.Enabled = false
	if err := s.cfg.Save(s.configPath); err != nil {
		*s.cfg = previous
		s.mu.Unlock()
		return nil, err
	}
	next := *s.cfg
	masked := s.cfg.Masked()
	s.mu.Unlock()
	s.shell.Configure(next)
	if s.runner != nil {
		s.runner.Configure(next)
	}
	return masked, nil
}

func (s *Server) serviceAccountState(ctx context.Context, status serviceaccount.Status) serviceaccount.Status {
	status.Account = managedServiceAccount
	credentialStatus := s.credential.Status()
	status.CredentialStored = credentialStatus.Stored
	status.CredentialScope = credentialStatus.Scope
	switch {
	case !status.Supported:
		status.State, status.Action = "unsupported", ""
	case status.Administrator:
		status.State, status.Action = "administrator", "Repair"
	case status.LockedOut:
		status.State, status.Action = "locked_out", "Repair"
	case !status.Exists:
		status.State, status.Action = "missing", "Set up"
	case !status.CredentialStored:
		if s.credentialRejected {
			status.State = "invalid_credential"
		} else {
			status.State = "missing_credential"
		}
		status.Action = "Repair"
	case !s.ConfigSnapshot().Shell.ServiceAccount.Enabled:
		// Item 2l2 (b): with the split off there is no identity in use, so nothing
		// tests it and no alarm is raised. This path ran the live test on every
		// Settings render, which is how ten approval alarms appeared in four
		// minutes for an account the product was not using.
		status.State, status.Action = "disabled", ""
	default:
		testContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := s.shellTest(testContext); errors.Is(err, tools.ErrServiceCredentialRejected) {
			_ = s.credential.Clear()
			s.credentialRejected = true
			status.State, status.Action = "invalid_credential", "Repair"
		} else if errors.Is(err, tools.ErrServiceAccountLocked) {
			status.LockedOut = true
			status.State, status.Action = "locked_out", "Repair"
		} else if err != nil {
			status.State, status.Action = "credential_check_failed", "Repair"
		} else {
			s.credentialRejected = false
			status.State, status.Action = "ready", ""
		}
	}
	return status
}

func (s *Server) previousCredential() ([]byte, bool, error) {
	if !s.credential.Status().Stored {
		return nil, false, nil
	}
	value, err := s.credential.Read()
	return value, err == nil, err
}

func (s *Server) restoreCredential(previous []byte, stored bool) error {
	if !stored {
		return s.credential.Clear()
	}
	return s.credential.Write(previous)
}

func (s *Server) enableConfiguredServiceAccount(account string, allowLocalNetwork bool, localSubnets []string) (any, error) {
	s.mu.Lock()
	previous := *s.cfg
	s.cfg.Shell.ServiceAccount.Account = account
	s.cfg.Shell.ServiceAccount.Domain = "."
	s.cfg.Shell.ServiceAccount.Enabled = true
	s.cfg.Shell.AllowLocalNetwork = allowLocalNetwork
	s.cfg.Shell.ConfirmedLocalSubnets = append([]string(nil), localSubnets...)
	if err := s.cfg.Save(s.configPath); err != nil {
		*s.cfg = previous
		s.mu.Unlock()
		return nil, err
	}
	next := *s.cfg
	masked := s.cfg.Masked()
	s.mu.Unlock()
	s.shell.Configure(next)
	if s.runner != nil {
		s.runner.Configure(next)
	}
	return masked, nil
}

func generatedServicePassword() ([]byte, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		clearSecret(value)
		return nil, fmt.Errorf("generate service-account password: %w", err)
	}
	encoded := make([]byte, base64.RawURLEncoding.EncodedLen(len(value)))
	base64.RawURLEncoding.Encode(encoded, value)
	clearSecret(value)
	return encoded, nil
}

func clearSecret(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
