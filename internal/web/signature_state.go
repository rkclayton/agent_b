package web

import (
	"context"
	"os"

	"harness/internal/buildinfo"
	"harness/internal/signing"
)

// signingState is read-only build identity for About. Release signing is an
// offline publisher operation; the running application exposes no signing API.
func (s *Server) signingState() signing.Status {
	s.signingMu.Lock()
	defer s.signingMu.Unlock()
	return s.signingStatus
}

func (s *Server) RefreshSigningState(ctx context.Context) error {
	status, err := s.signing.Status(ctx, signing.Request{ExpectedHash: buildinfo.Current().ExecutableSHA256, ProcessID: os.Getpid()})
	if err != nil {
		return err
	}
	s.signingMu.Lock()
	s.signingStatus = status
	s.signingMu.Unlock()
	return nil
}
