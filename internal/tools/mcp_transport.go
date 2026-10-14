package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"harness/internal/config"
)

type stdioMCPEndpoint struct {
	process runningShellProcess
	output  *lockedBuffer
	done    chan struct{}
	mu      sync.Mutex
	offset  int64
	pending string
	limit   int
}

func startStdioMCP(shell *Shell, command, workspace string, limit int) (*stdioMCPEndpoint, error) {
	cfg := shell.config()
	if denied := deniedCommand(command, cfg.Deny); denied != "" {
		return nil, errors.New("command blocked by deny list")
	}
	host := shellHostFor(cfg)
	executed := shellCommandForHost(host, command)
	argv := append(append([]string(nil), cfg.Command[1:]...), executed)
	output := newProcessWindow(1 << 20)
	process, _, err := shell.startBackground(cfg, workspace, argv, output, false)
	if err != nil {
		return nil, err
	}
	endpoint := &stdioMCPEndpoint{process: process, output: output, done: make(chan struct{}), limit: limit}
	go func() { _, _ = process.Wait(); close(endpoint.done) }()
	return endpoint, nil
}

func (e *stdioMCPEndpoint) write(payload []byte) error {
	interactive, ok := e.process.(interactiveShellProcess)
	if !ok {
		return errors.New("MCP server input is closed")
	}
	line := append(append([]byte(nil), payload...), '\n')
	return interactive.WriteInput(line)
}
func (e *stdioMCPEndpoint) Exchange(ctx context.Context, payload []byte) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(payload) > 0 {
		if err := e.write(payload); err != nil {
			return nil, err
		}
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		value, used, next := e.output.Read(e.offset)
		e.offset = next
		if used > next-int64(len(value)) {
			e.pending = ""
		}
		e.pending += value
		if index := strings.IndexByte(e.pending, '\n'); index >= 0 {
			line := strings.TrimSpace(e.pending[:index])
			e.pending = e.pending[index+1:]
			if line == "" {
				continue
			}
			// The shared process launcher captures stderr in the same bounded window.
			// MCP permits diagnostic stderr, so discard non-protocol lines here.
			if !json.Valid([]byte(line)) {
				continue
			}
			if e.limit > 0 && len(line) > e.limit {
				return nil, fmt.Errorf("MCP response exceeds max_body_kb")
			}
			return []byte(line), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.done:
			return nil, errors.New("MCP server exited")
		case <-ticker.C:
		}
	}
}
func (e *stdioMCPEndpoint) Notify(_ context.Context, payload []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.write(payload)
}
func (e *stdioMCPEndpoint) Close() error {
	e.process.KillTree()
	select {
	case <-e.done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("MCP server did not stop")
	}
}

func mcpCommand(value string) (string, bool) {
	command, ok := strings.CutPrefix(strings.TrimSpace(value), "command:")
	return strings.TrimSpace(command), ok && strings.TrimSpace(command) != ""
}

func validMCPServiceTarget(service config.Service) bool {
	_, command := mcpCommand(service.BaseURL)
	return command || strings.HasPrefix(service.BaseURL, "http://") || strings.HasPrefix(service.BaseURL, "https://")
}
