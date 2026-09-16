package web

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"harness/internal/buildinfo"
	"harness/internal/events"
	"harness/internal/signing"
)

func (s *Server) signingState() signing.Status {
	s.signingMu.Lock()
	defer s.signingMu.Unlock()
	status := s.signingStatus
	if status.Thumbprint == "" {
		status.Thumbprint = s.ConfigSnapshot().Signing.Thumbprint
		status.Configured = status.Thumbprint != ""
	}
	return status
}

func (s *Server) RefreshSigningState(ctx context.Context) error {
	cfg := s.ConfigSnapshot()
	status, err := s.signing.Status(ctx, signing.Request{Thumbprint: cfg.Signing.Thumbprint, TimestampURL: cfg.Signing.TimestampURL, ExpectedHash: buildinfo.Current().ExecutableSHA256, ProcessID: os.Getpid()})
	if err != nil { return err }
	s.signingMu.Lock(); s.signingStatus = status; s.signingMu.Unlock()
	return nil
}

func (s *Server) codeSigning(w http.ResponseWriter, r *http.Request) {
	if s.signing == nil {
		writeError(w, http.StatusConflict, "code-signing runtime is unavailable", "signing")
		return
	}
	cfg := s.ConfigSnapshot()
	request := signing.Request{Thumbprint: cfg.Signing.Thumbprint, TimestampURL: cfg.Signing.TimestampURL, ExpectedHash: buildinfo.Current().ExecutableSHA256, ProcessID: os.Getpid()}
	if r.Method == http.MethodGet {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		status, err := s.signing.Status(ctx, request)
		if err != nil {
			if errors.Is(err, signing.ErrUnsupported) {
				writeError(w, http.StatusNotImplemented, err.Error(), "signing")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error(), "signing")
			return
		}
		s.signingMu.Lock()
		s.signingStatus = status
		s.signingMu.Unlock()
		writeJSON(w, http.StatusOK, status)
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if err := s.operatorRequest(r); err != nil {
		writeError(w, http.StatusForbidden, "code-signing changes require a verified operator browser process: "+err.Error(), "signing")
		return
	}
	manageStatus, statusErr := s.signing.Status(r.Context(), request)
	if statusErr != nil {
		writeError(w, http.StatusInternalServerError, statusErr.Error(), "signing")
		return
	}
	if !manageStatus.CanManage {
		writeError(w, http.StatusForbidden, "this Windows account may verify signatures but may not manage signing", "signing")
		return
	}
	var body struct {
		Action     string `json:"action"`
		Thumbprint string `json:"thumbprint"`
		PFXBase64  string `json:"pfx_base64"`
		Password   string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	request.Thumbprint = strings.TrimSpace(body.Thumbprint)
	if request.Thumbprint == "" {
		request.Thumbprint = cfg.Signing.Thumbprint
	}
	if body.PFXBase64 != "" {
		value, err := base64.StdEncoding.DecodeString(body.PFXBase64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "PFX is not valid base64", "signing.pfx")
			return
		}
		request.PFX = value
	}
	request.Password = []byte(body.Password)
	body.Password = ""
	defer func() { clearSigningBytes(request.Password); clearSigningBytes(request.PFX) }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var result signing.Result
	var err error
	switch body.Action {
	case "create":
		result, err = s.signing.Create(ctx, request)
	case "import":
		result, err = s.signing.Import(ctx, request)
	case "select":
		result, err = s.signing.Select(ctx, request)
	case "sign":
		if s.registry != nil {
			for _, item := range s.registry.List() {
				if item.IsRunning() {
					writeError(w, http.StatusConflict, "finish or stop active sessions before signing and restarting Agent_b", "signing")
					return
				}
			}
		}
		if request.Thumbprint == "" {
			writeError(w, http.StatusBadRequest, "select or create a certificate before signing", "signing.thumbprint")
			return
		}
		result, err = s.signing.Sign(ctx, request)
	case "export":
		certificate, exportErr := s.signing.Export(ctx, request)
		if exportErr != nil {
			err = exportErr
			break
		}
		w.Header().Set("Content-Type", "application/pkix-cert")
		w.Header().Set("Content-Disposition", `attachment; filename="Agent_b-code-signing.cer"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(certificate)
		return
	default:
		writeError(w, http.StatusBadRequest, "action must be create, import, select, sign, or export", "signing.action")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "signing")
		return
	}
	if result.Thumbprint != "" && body.Action != "sign" {
		if err := s.saveSigningThumbprint(result.Thumbprint); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "signing.thumbprint")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result, "config": s.ConfigSnapshot().Masked()})
}

func (s *Server) saveSigningThumbprint(thumbprint string) error {
	s.mu.Lock()
	next := *s.cfg
	next.Signing.Thumbprint = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(thumbprint), " ", ""))
	if err := next.Save(s.configPath); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("save signing certificate: %w", err)
	}
	s.cfg = &next
	s.mu.Unlock()
	s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": next.Masked()}))
	return nil
}

func clearSigningBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
