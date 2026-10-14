package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const diagnosticOutputLimit = 1 << 20

type languageDiagnostics struct {
	shell         *Shell
	mu            sync.Mutex
	commands      map[string][]string
	servers       map[string]*languageServer
	timeout, idle time.Duration
	done          chan struct{}
}

type languageServer struct {
	mu       sync.Mutex
	process  runningShellProcess
	output   *lockedBuffer
	cursor   int64
	pending  string
	last     time.Time
	nextID   int
	versions map[string]int
}

type lspMessage struct {
	ID     any    `json:"id"`
	Method string `json:"method"`
	Params struct {
		URI         string          `json:"uri"`
		Diagnostics []lspDiagnostic `json:"diagnostics"`
	} `json:"params"`
}

type lspDiagnostic struct {
	Range struct {
		Start struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"start"`
	} `json:"range"`
	Severity int    `json:"severity"`
	Message  string `json:"message"`
}

func NewLanguageDiagnostics(shell *Shell, commands map[string][]string) *languageDiagnostics {
	m := &languageDiagnostics{shell: shell, commands: commands, servers: map[string]*languageServer{}, timeout: 3 * time.Second, idle: 10 * time.Minute, done: make(chan struct{})}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.ReapIdle()
			case <-m.done:
				return
			}
		}
	}()
	return m
}

func (m *languageDiagnostics) Check(ctx context.Context, root, path string) (string, bool) {
	command := m.commands[strings.ToLower(filepath.Ext(path))]
	if len(command) == 0 {
		return "", false
	}
	executable, err := exec.LookPath(command[0])
	if err != nil {
		return "", false
	}
	key := filepath.Clean(root) + "\x00" + strings.Join(command, "\x00")
	m.mu.Lock()
	server := m.servers[key]
	if server == nil {
		if len(m.servers) >= 16 {
			m.evictOldestLocked()
		}
		output := newProcessWindow(diagnosticOutputLimit)
		process, startErr := m.start(executable, command[1:], root, output)
		if startErr == nil {
			server = &languageServer{process: process, output: output, last: time.Now(), nextID: 1, versions: map[string]int{}}
			m.servers[key] = server
			go func() { _, _ = process.Wait() }()
		}
	}
	m.mu.Unlock()
	if server == nil {
		return "diagnostics: no diagnostics in 3 s", true
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	server.last = time.Now()
	if err := server.initialize(ctx, root, m.timeout); err != nil {
		return "diagnostics: no diagnostics in 3 s", true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "diagnostics: no diagnostics in 3 s", true
	}
	uri := fileURI(path)
	version := server.versions[uri] + 1
	server.versions[uri] = version
	if version == 1 {
		server.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": languageID(path), "version": version, "text": string(data)}})
	} else {
		server.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": version}, "contentChanges": []any{map[string]any{"text": string(data)}}})
	}
	message, err := server.wait(ctx, m.timeout, func(message lspMessage) bool {
		return message.Method == "textDocument/publishDiagnostics" && message.Params.URI == uri
	})
	if err != nil {
		return "diagnostics: no diagnostics in 3 s", true
	}
	return formatDiagnostics(message.Params.Diagnostics), true
}

func (m *languageDiagnostics) start(executable string, args []string, root string, output *lockedBuffer) (runningShellProcess, error) {
	cfg := m.shell.config()
	service := cfg.ServiceAccount
	if cfg.OperatorContext || !service.Enabled {
		return startHarnessBackgroundProcess(executable, args, root, output)
	}
	if m.shell.credential == nil {
		return nil, errors.New("service-account credential is unavailable")
	}
	password, err := m.shell.credential.Read()
	if err != nil {
		return nil, err
	}
	defer clearBytes(password)
	return startServiceAccountBackgroundProcess(executable, args, root, minimalShellEnvironment(service, root), service, password, output)
}

func (s *languageServer) initialize(ctx context.Context, root string, timeout time.Duration) error {
	if s.nextID > 1 {
		return nil
	}
	id := s.nextID
	s.nextID++
	if err := s.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "initialize", "params": map[string]any{"processId": os.Getpid(), "rootUri": fileURI(root), "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "agent_b"}}}); err != nil {
		return err
	}
	if _, err := s.wait(ctx, timeout, func(message lspMessage) bool { return numericID(message.ID) == id }); err != nil {
		return err
	}
	return s.notify("initialized", map[string]any{})
}

func (s *languageServer) notify(method string, params any) error {
	return s.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
func (s *languageServer) send(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.process.(interactiveShellProcess).WriteInput([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(data), data)))
}

func (s *languageServer) wait(ctx context.Context, timeout time.Duration, accept func(lspMessage) bool) (lspMessage, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if message, ok := s.next(accept); ok {
			return message, nil
		}
		select {
		case <-ctx.Done():
			return lspMessage{}, ctx.Err()
		case <-deadline.C:
			return lspMessage{}, errors.New("timeout")
		case <-ticker.C:
		}
	}
}

func (s *languageServer) next(accept func(lspMessage) bool) (lspMessage, bool) {
	chunk, base, next := s.output.Read(s.cursor)
	if base > s.cursor {
		s.pending = ""
	}
	s.cursor = next
	s.pending += chunk
	if len(s.pending) > diagnosticOutputLimit {
		s.pending = s.pending[len(s.pending)-diagnosticOutputLimit:]
	}
	for {
		head := strings.Index(s.pending, "\r\n\r\n")
		if head < 0 {
			return lspMessage{}, false
		}
		length := -1
		for _, line := range strings.Split(s.pending[:head], "\r\n") {
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				length, _ = strconv.Atoi(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
			}
		}
		if length < 0 {
			s.pending = s.pending[head+4:]
			continue
		}
		if len(s.pending) < head+4+length {
			return lspMessage{}, false
		}
		body := s.pending[head+4 : head+4+length]
		s.pending = s.pending[head+4+length:]
		var message lspMessage
		if json.Unmarshal([]byte(body), &message) == nil && accept(message) {
			return message, true
		}
	}
}

func formatDiagnostics(values []lspDiagnostic) string {
	if len(values) == 0 {
		return ""
	}
	limit := min(20, len(values))
	lines := make([]string, 0, limit+2)
	lines = append(lines, "diagnostics:")
	for _, value := range values[:limit] {
		severity := map[int]string{1: "error", 2: "warning", 3: "information", 4: "hint"}[value.Severity]
		if severity == "" {
			severity = "diagnostic"
		}
		message := strings.Join(strings.Fields(value.Message), " ")
		lines = append(lines, fmt.Sprintf("%d:%d %s %s", value.Range.Start.Line+1, value.Range.Start.Character+1, severity, message))
	}
	if rest := len(values) - limit; rest > 0 {
		lines = append(lines, fmt.Sprintf("%d more", rest))
	}
	return strings.Join(lines, "\n")
}

func fileURI(path string) string {
	absolute, _ := filepath.Abs(path)
	path = filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}
func languageID(path string) string {
	return map[string]string{".go": "go", ".ts": "typescript", ".tsx": "typescriptreact", ".js": "javascript", ".jsx": "javascriptreact", ".py": "python", ".cs": "csharp", ".rs": "rust"}[strings.ToLower(filepath.Ext(path))]
}
func numericID(value any) int {
	if number, ok := value.(float64); ok {
		return int(number)
	}
	return -1
}

func (m *languageDiagnostics) ReapIdle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for key, server := range m.servers {
		if now.Sub(server.last) >= m.idle {
			server.process.KillTree()
			delete(m.servers, key)
		}
	}
}
func (m *languageDiagnostics) evictOldestLocked() {
	var key string
	var oldest time.Time
	for candidate, server := range m.servers {
		if key == "" || server.last.Before(oldest) {
			key, oldest = candidate, server.last
		}
	}
	if key != "" {
		m.servers[key].process.KillTree()
		delete(m.servers, key)
	}
}
func (m *languageDiagnostics) Active() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.servers) }
func (m *languageDiagnostics) Close() {
	select {
	case <-m.done:
		return
	default:
		close(m.done)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, server := range m.servers {
		server.process.KillTree()
		delete(m.servers, key)
	}
}
