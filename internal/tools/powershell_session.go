package tools

import (
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/identity"
	"harness/internal/session"
)

const (
	powerShellCommandLimit = 120 * time.Second
	powerShellIdleLimit    = 30 * time.Minute
	powerShellOutputWindow = 4 << 20
	powerShellReadOnlyRule = "error: read-only profile: <command>: <reason>"
)

//go:embed powershell_prelude.ps1
var powerShellPrelude string

var powerShellSessionName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type persistentPowerShellProcess struct {
	process  runningShellProcess
	input    interactiveShellProcess
	output   *lockedBuffer
	offset   int64
	done     chan struct{}
	lastUsed atomic.Int64
	mu       sync.Mutex
}

type powerShellCommandFailure string

func (e powerShellCommandFailure) Error() string { return string(e) }

type PowerShellSession struct {
	shell       *Shell
	vault       *credential.Vault
	tokens      identity.TokenProvider
	profileRoot func() string

	mu           sync.Mutex
	sessions     map[string]*persistentPowerShellProcess
	commandLimit time.Duration
	idleLimit    time.Duration
	sweepEvery   time.Duration
	stop         chan struct{}
	closed       chan struct{}
	activeRoot   string
}

func NewPowerShellSession(shell *Shell, vault *credential.Vault, tokens identity.TokenProvider, profileRoot func() string) *PowerShellSession {
	if shell == nil || vault == nil || tokens == nil || profileRoot == nil {
		return nil
	}
	host := shellHostFor(shell.config())
	if host.Version == "" || host.Executable == "" {
		return nil
	}
	if _, err := os.Stat(host.Executable); err != nil {
		return nil
	}
	tool := &PowerShellSession{
		shell: shell, vault: vault, tokens: tokens, profileRoot: profileRoot,
		sessions: map[string]*persistentPowerShellProcess{}, commandLimit: powerShellCommandLimit,
		idleLimit: powerShellIdleLimit, sweepEvery: time.Minute, stop: make(chan struct{}), closed: make(chan struct{}),
		activeRoot: filepath.Clean(profileRoot()),
	}
	go tool.sweep()
	return tool
}

func (*PowerShellSession) Name() string { return "powershell_session" }
func (p *PowerShellSession) Description() string {
	return "Run a command in a named persistent PowerShell session. The active agent profile decides whether this tool is read-only or full. Available sessions: " + strings.Join(p.names(), ", ") + "."
}
func (p *PowerShellSession) DescriptionFor(enabled map[string]bool) string {
	mode := "Read only"
	if enabled["powershell_session:full"] {
		mode = "Full"
	}
	return "Run a command in a named persistent PowerShell session. Mode: " + mode + ". Available sessions: " + strings.Join(p.names(), ", ") + "."
}
func (*PowerShellSession) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"command": map[string]any{"type": "string"},
		"session": map[string]any{"type": "string", "default": "default"},
	}, "required": []string{"command"}}
}

func (p *PowerShellSession) names() []string {
	seen := map[string]bool{"default": true}
	directory := filepath.Join(p.profileRoot(), "powershell")
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".ps1") && powerShellSessionName.MatchString(name) {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (p *PowerShellSession) Call(ctx context.Context, item *session.Session, args map[string]any) (string, error) {
	command, _ := args["command"].(string)
	if strings.TrimSpace(command) == "" {
		return "", errors.New("command is required")
	}
	name, _ := args["session"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		name = "default"
	}
	if !powerShellSessionName.MatchString(name) {
		return "", errors.New("session must be lower-case letters, digits and hyphens")
	}
	full := item.ToolEnabled("powershell_session:full")
	result, err := p.run(ctx, item, name, command, full, false)
	if (err == nil && expiredPowerShellToken(result)) || (err != nil && expiredPowerShellToken(err.Error())) {
		p.closeKey(p.key(item.ID, item.AgentID, name))
		return p.run(ctx, item, name, command, full, true)
	}
	return result, err
}

func (p *PowerShellSession) run(ctx context.Context, item *session.Session, name, command string, full, retried bool) (string, error) {
	key := p.key(item.ID, item.AgentID, name)
	process, err := p.open(ctx, item, name)
	if err != nil {
		return "", err
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	if !full {
		refusal, checkErr := p.exchange(ctx, process, readOnlyWrapper(command), p.commandLimit)
		if checkErr != nil {
			p.closeKey(key)
			return "", checkErr
		}
		if strings.HasPrefix(strings.TrimSpace(refusal), "read-only profile:") {
			return "", errors.New(strings.TrimSpace(refusal))
		}
	}
	result, callErr := p.exchange(ctx, process, commandWrapper(command), p.commandLimit)
	process.lastUsed.Store(time.Now().UnixNano())
	if callErr != nil {
		var commandFailure powerShellCommandFailure
		if errors.As(callErr, &commandFailure) {
			if retried && expiredPowerShellToken(callErr.Error()) {
				p.closeKey(key)
				return "", fmt.Errorf("persistent PowerShell session %s still reports an expired or invalid token after one refresh", name)
			}
			return "", commandFailure
		}
		p.closeKey(key)
		if errors.Is(callErr, context.DeadlineExceeded) {
			return "", fmt.Errorf("persistent PowerShell session %s reached the %s command limit: %w", name, p.commandLimit, callErr)
		}
		return "", fmt.Errorf("persistent PowerShell session %s ended: %w", name, callErr)
	}
	if retried && expiredPowerShellToken(result) {
		p.closeKey(key)
		return "", fmt.Errorf("persistent PowerShell session %s still reports an expired or invalid token after one refresh", name)
	}
	return strings.TrimRight(result, "\r\n"), nil
}

func (p *PowerShellSession) key(sessionID, agentID, name string) string {
	return sessionID + "\x00" + agentID + "\x00" + name
}

func (p *PowerShellSession) open(ctx context.Context, item *session.Session, name string) (*persistentPowerShellProcess, error) {
	key := p.key(item.ID, item.AgentID, name)
	p.mu.Lock()
	if current := p.sessions[key]; current != nil {
		if time.Since(time.Unix(0, current.lastUsed.Load())) < p.idleLimit {
			p.mu.Unlock()
			return current, nil
		}
		delete(p.sessions, key)
		current.process.KillTree()
	}
	p.mu.Unlock()

	bootstrap, credentials, err := p.bootstrap(name)
	if err != nil {
		return nil, err
	}
	output := newProcessWindow(powerShellOutputWindow)
	host := shellHostFor(p.shell.config())
	argv := []string{"-NoLogo", "-NoProfile", "-NonInteractive"}
	if host.Version == "5.1" {
		argv = append(argv, "-WindowStyle", "Hidden")
	}
	argv = append(argv, "-Command", "-")
	running, _, err := p.shell.startBackground(p.shell.config(), item.Workspace, argv, output, false)
	if err != nil {
		return nil, err
	}
	input, ok := running.(interactiveShellProcess)
	if !ok {
		running.KillTree()
		return nil, errors.New("persistent PowerShell host has no input pipe")
	}
	process := &persistentPowerShellProcess{process: running, input: input, output: output, done: make(chan struct{})}
	process.lastUsed.Store(time.Now().UnixNano())
	go func() { _, _ = running.Wait(); close(process.done) }()

	initial := "$AgentB = [pscustomobject]@{}\n" + powerShellPrelude
	for _, credentialName := range credentials {
		value, entry, secretErr := p.vault.Secret(credentialName)
		if secretErr != nil {
			running.KillTree()
			return nil, errors.New("auth_error: sign in in Settings → Security → Credentials")
		}
		if entry.Kind == "entra" {
			value, secretErr = p.tokens.Token(ctx, credentialName)
		}
		if secretErr != nil || (entry.Kind != "entra" && entry.Kind != "key") {
			running.KillTree()
			return nil, errors.New("auth_error: sign in in Settings → Security → Credentials")
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(value))
		initial += fmt.Sprintf("\n$AgentB | Add-Member -NotePropertyName '%s' -NotePropertyValue ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s'))) -Force", credentialName, encoded)
	}
	if strings.TrimSpace(bootstrap) != "" {
		initial += "\n" + bootstrap
	}
	openLimit := max(p.commandLimit, 10*time.Second)
	if _, err := p.exchange(ctx, process, commandWrapper(initial), openLimit); err != nil {
		running.KillTree()
		return nil, fmt.Errorf("open persistent PowerShell session %s: %w", name, err)
	}
	p.mu.Lock()
	if existing := p.sessions[key]; existing != nil {
		p.mu.Unlock()
		running.KillTree()
		return existing, nil
	}
	p.sessions[key] = process
	p.mu.Unlock()
	return process, nil
}

func (p *PowerShellSession) bootstrap(name string) (string, []string, error) {
	path := filepath.Join(p.profileRoot(), "powershell", name+".ps1")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && name == "default" {
		return "", nil, nil
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("persistent PowerShell session %q has no bootstrap file", name)
		}
		return "", nil, fmt.Errorf("read persistent PowerShell bootstrap: %w", err)
	}
	if len(raw) > 1<<20 {
		return "", nil, errors.New("persistent PowerShell bootstrap exceeds 1 MiB")
	}
	first, _, _ := strings.Cut(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	credentials := []string{}
	if value, found := strings.CutPrefix(strings.TrimSpace(first), "# credentials:"); found {
		for _, name := range strings.Split(value, ",") {
			name = strings.TrimSpace(name)
			if !powerShellSessionName.MatchString(name) {
				return "", nil, fmt.Errorf("invalid credential name %q in bootstrap", name)
			}
			credentials = append(credentials, name)
		}
	}
	return string(raw), credentials, nil
}

func (p *PowerShellSession) exchange(ctx context.Context, process *persistentPowerShellProcess, script string, limit time.Duration) (string, error) {
	marker := fmt.Sprintf("__AGENTB_PS_END_%d_", time.Now().UnixNano())
	okMarker, errorMarker := marker+"OK__", marker+"ERROR__"
	line := script + "\n$__agentb_ok = $? -and $Error.Count -eq 0; if ($__agentb_ok) { Write-Output '" + okMarker + "' } else { Write-Output '" + errorMarker + "' }\n"
	if err := process.input.WriteInput([]byte(line)); err != nil {
		return "", err
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
			return "", context.DeadlineExceeded
		case <-process.done:
			return "", errors.New("PowerShell host exited")
		case <-ticker.C:
			chunk, adjusted, total := process.output.Read(process.offset)
			okAt, errorAt := strings.Index(chunk, okMarker), strings.Index(chunk, errorMarker)
			at, commandFailed := okAt, false
			if errorAt >= 0 && (at < 0 || errorAt < at) {
				at, commandFailed = errorAt, true
			}
			if at >= 0 {
				prefix := ""
				if adjusted > process.offset {
					prefix = fmt.Sprintf("[persistent PowerShell output cut: %d earlier bytes omitted]\n", adjusted-process.offset)
				}
				process.offset = total
				result := prefix + strings.TrimSuffix(strings.TrimSuffix(chunk[:at], "\n"), "\r")
				if commandFailed {
					message := strings.TrimSpace(result)
					if message == "" {
						message = "PowerShell command failed"
					}
					return "", powerShellCommandFailure(message)
				}
				return result, nil
			}
		}
	}
}

func commandWrapper(command string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(command))
	return "$Error.Clear(); $__agentb_text=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + encoded + "')); Invoke-Expression $__agentb_text"
}

func readOnlyWrapper(command string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(command))
	return "$Error.Clear(); $__agentb_text=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + encoded + "')); $result=Test-AgentBReadOnlyCommand -Text $__agentb_text; if ($result) { Write-Output $result }"
}

func expiredPowerShellToken(output string) bool {
	value := strings.ToLower(output)
	return strings.Contains(value, "expired token") || strings.Contains(value, "token has expired") || strings.Contains(value, "invalid token") || strings.Contains(value, "invalidauthenticationtoken")
}

func (p *PowerShellSession) closeKey(key string) {
	p.mu.Lock()
	process := p.sessions[key]
	delete(p.sessions, key)
	p.mu.Unlock()
	if process != nil {
		process.process.KillTree()
	}
}

func (p *PowerShellSession) CloseSession(sessionID string) {
	p.mu.Lock()
	keys := []string{}
	for key := range p.sessions {
		if strings.HasPrefix(key, sessionID+"\x00") {
			keys = append(keys, key)
		}
	}
	p.mu.Unlock()
	for _, key := range keys {
		p.closeKey(key)
	}
}

func (p *PowerShellSession) ProfileChanged() {
	next := filepath.Clean(p.profileRoot())
	p.mu.Lock()
	if strings.EqualFold(next, p.activeRoot) {
		p.mu.Unlock()
		return
	}
	p.activeRoot = next
	p.mu.Unlock()
	p.closeAll()
}

func (p *PowerShellSession) closeAll() {
	p.mu.Lock()
	keys := make([]string, 0, len(p.sessions))
	for key := range p.sessions {
		keys = append(keys, key)
	}
	p.mu.Unlock()
	for _, key := range keys {
		p.closeKey(key)
	}
}

func (p *PowerShellSession) sweep() {
	defer close(p.closed)
	for {
		timer := time.NewTimer(p.sweepEvery)
		select {
		case <-p.stop:
			timer.Stop()
			return
		case now := <-timer.C:
			p.closeIdle(now)
		}
	}
}

func (p *PowerShellSession) closeIdle(now time.Time) {
	p.mu.Lock()
	keys := []string{}
	for key, process := range p.sessions {
		if now.Sub(time.Unix(0, process.lastUsed.Load())) >= p.idleLimit {
			keys = append(keys, key)
		}
	}
	p.mu.Unlock()
	for _, key := range keys {
		p.closeKey(key)
	}
}

func (p *PowerShellSession) Close() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	<-p.closed
	p.closeAll()
}

func (p *PowerShellSession) Configure(config.Config) {}
