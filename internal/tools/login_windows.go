//go:build windows

package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"harness/internal/credential"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const loginPipeEnv = "AGENTB_SSH_ASKPASS_PIPE"

type sshLoginRequest struct {
	Prompt string
	Port   int
}
type sshLoginAttempt struct {
	created           int64
	name, user, host  string
	answered, refused bool
}
type sshLoginCall struct {
	ctx         context.Context
	cancel      context.CancelFunc
	vault       *credential.Vault
	output      *lockedBuffer
	path, exe   string
	armed       chan struct{}
	done        chan struct{}
	root        uint32
	rootCreated int64
	mu          sync.Mutex
	pipe        windows.Handle
	attempts    map[uint32]*sshLoginAttempt
	notices     map[string]bool
	ready       chan error
}

func init() { newLoginCall = startSSHLogin; SSHAskpassMain = sshAskpassMain }
func startSSHLogin(ctx context.Context, v *credential.Vault, out *lockedBuffer) (loginCall, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	c := &sshLoginCall{ctx: ctx, cancel: cancel, vault: v, output: out, path: `\\.\pipe\AgentB-login-` + hex.EncodeToString(id), exe: exe, armed: make(chan struct{}), done: make(chan struct{}), attempts: map[uint32]*sshLoginAttempt{}, notices: map[string]bool{}, ready: make(chan error, 1)}
	out.ring = make([]byte, 1<<20)
	go c.serve()
	if err = <-c.ready; err != nil {
		cancel()
		return nil, err
	}
	return c, nil
}
func (c *sshLoginCall) Environment() []string {
	return []string{"SSH_ASKPASS=" + c.exe, "SSH_ASKPASS_REQUIRE=force", loginPipeEnv + "=" + c.path}
}
func (c *sshLoginCall) Arm(pid int) {
	c.root = uint32(pid)
	h, _, created, _, err := loginProcess(c.root)
	if err == nil {
		c.rootCreated = created
		windows.CloseHandle(h)
	}
	close(c.armed)
}
func (c *sshLoginCall) Close() {
	c.cancel()
	c.mu.Lock()
	if c.pipe != 0 {
		windows.CancelIoEx(c.pipe, nil)
		windows.CloseHandle(c.pipe)
		c.pipe = 0
	}
	c.mu.Unlock()
	<-c.done
}
func (c *sshLoginCall) Result(d CallDetail) CallDetail {
	for _, a := range c.attempts {
		if a.answered {
			c.notice("login: " + a.name)
			if a.refused || (d.Err != nil && (strings.Contains(d.Err.Error(), "Permission denied (") || strings.Contains(d.Err.Error(), "Too many authentication failures"))) {
				c.notice("stored login for " + a.user + "@" + a.host + " was refused")
			}
		}
	}
	lines := make([]string, 0, len(c.notices))
	for line := range c.notices {
		lines = append(lines, line)
	}
	sort.Strings(lines)
	if len(lines) > 0 {
		notice := strings.Join(lines, "\n")
		if d.Err != nil {
			d.Err = fmt.Errorf("%w\n%s", d.Err, notice)
		} else {
			d.Content = strings.TrimSpace(d.Content + "\n" + notice)
		}
	}
	return d
}
func (c *sshLoginCall) notice(line string) {
	if len(c.notices) < 256 {
		c.notices[line] = true
	}
}
func (c *sshLoginCall) serve() {
	defer close(c.done)
	defer func() {
		select {
		case c.ready <- errors.New("login helper closed"):
		default:
		}
	}()
	// The pipe carries no authorization through its name: Windows supplies the
	// client PID, then live handles bind the helper, SSH and this call's shell.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GRGW;;;AU)")
	if err != nil {
		c.ready <- err
		return
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	for c.ctx.Err() == nil {
		name, _ := windows.UTF16PtrFromString(c.path)
		h, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_FIRST_PIPE_INSTANCE, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 32768, 8192, 0, &sa)
		if err != nil {
			select {
			case c.ready <- err:
			default:
			}
			return
		}
		c.mu.Lock()
		if c.ctx.Err() != nil {
			windows.CloseHandle(h)
			c.mu.Unlock()
			return
		}
		c.pipe = h
		select {
		case c.ready <- nil:
		default:
		}
		c.mu.Unlock()
		err = windows.ConnectNamedPipe(h, nil)
		if err == nil || errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			timer := time.AfterFunc(5*time.Second, func() { windows.CancelIoEx(h, nil) })
			c.answer(h)
			timer.Stop()
		}
		c.mu.Lock()
		if c.pipe == h {
			windows.DisconnectNamedPipe(h)
			windows.CloseHandle(h)
			c.pipe = 0
		}
		c.mu.Unlock()
	}
}
func (c *sshLoginCall) answer(pipe windows.Handle) {
	var client uint32
	if windows.GetNamedPipeClientProcessId(pipe, &client) != nil {
		return
	}
	helper, parent, helperCreated, image, err := loginProcess(client)
	if err != nil {
		return
	}
	defer windows.CloseHandle(helper)
	if !sameLoginImage(image, c.exe) {
		return
	}
	sshHandle, ancestor, created, sshImage, err := loginProcess(parent)
	if err != nil {
		return
	}
	defer windows.CloseHandle(sshHandle)
	if helperCreated < created || !sameLoginImage(sshImage, windowsSSH()) {
		return
	}
	select {
	case <-c.armed:
	case <-c.ctx.Done():
		return
	}
	found := false
	childCreated := created
	for i := 0; i < 32 && ancestor != 0; i++ {
		h, next, at, _, e := loginProcess(ancestor)
		if e != nil {
			return
		}
		windows.CloseHandle(h)
		if at > childCreated {
			return
		}
		childCreated = at
		if ancestor == c.root {
			found = at == c.rootCreated
			break
		}
		ancestor = next
	}
	if !found {
		return
	}
	answered := false
	defer func() {
		if !answered {
			stop, e := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, parent)
			if e == nil {
				defer windows.CloseHandle(stop)
				var at, exit, kernel, user windows.Filetime
				if windows.GetProcessTimes(stop, &at, &exit, &kernel, &user) == nil && at.Nanoseconds() == created {
					windows.TerminateProcess(stop, 1)
				}
			}
		}
	}()
	var duplicate windows.Handle
	if windows.DuplicateHandle(windows.CurrentProcess(), pipe, windows.CurrentProcess(), &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS) != nil {
		return
	}
	file := os.NewFile(uintptr(duplicate), "login-pipe")
	defer file.Close()
	var request sshLoginRequest
	if json.NewDecoder(io.LimitReader(file, 8192)).Decode(&request) != nil {
		return
	}
	if c.ctx.Value(loginSourceKey{}) == "phone" {
		return
	}
	prompt := strings.TrimSpace(request.Prompt)
	if !strings.HasSuffix(prompt, "'s password:") {
		return
	}
	target := strings.TrimSuffix(prompt, "'s password:")
	user, host, ok := strings.Cut(target, "@")
	if !ok || request.Port < 1 || request.Port > 65535 {
		return
	}
	attempt := c.attempts[parent]
	if attempt != nil && attempt.created == created {
		if attempt.answered {
			attempt.refused = true
		}
		return
	}
	for pid, a := range c.attempts {
		h, _, at, _, e := loginProcess(pid)
		if e == nil {
			windows.CloseHandle(h)
		}
		if e != nil || at != a.created {
			if a.answered {
				c.notice("login: " + a.name)
				if a.refused {
					c.notice("stored login for " + a.user + "@" + a.host + " was refused")
				}
			}
			delete(c.attempts, pid)
		}
	}
	if len(c.attempts) >= 256 {
		return
	}
	attempt = &sshLoginAttempt{created: created, user: user, host: host}
	c.attempts[parent] = attempt
	name, err := c.vault.LoginName(user, host, request.Port)
	if err != nil {
		return
	}
	if name == "" {
		c.notice("no stored login for " + target)
		return
	}
	secret, err := c.vault.LoginPassword(name, user, host, request.Port)
	if err != nil {
		return
	}
	defer clearBytes(secret)
	c.output.mu.Lock()
	seen := false
	for _, saved := range c.output.loginSecrets {
		if saved == string(secret) {
			seen = true
			break
		}
	}
	if !seen {
		if len(c.output.loginSecrets) >= 256 {
			c.output.mu.Unlock()
			return
		}
		c.output.loginSecrets = append(c.output.loginSecrets, string(secret))
	}
	c.output.mu.Unlock()
	attempt.name = name
	attempt.answered = true
	answered = true
	file.Write(append(secret, '\n'))
	windows.FlushFileBuffers(pipe)
}
func windowsSSH() string { return filepath.Join(os.Getenv("WINDIR"), "System32", "OpenSSH", "ssh.exe") }
func sameLoginImage(a, b string) bool {
	x, e := os.Stat(a)
	if e != nil {
		return false
	}
	y, e := os.Stat(b)
	return e == nil && os.SameFile(x, y)
}
func loginProcess(pid uint32) (windows.Handle, uint32, int64, string, error) {
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if e != nil {
		return 0, 0, 0, "", e
	}
	var basic windows.PROCESS_BASIC_INFORMATION
	var size uint32
	e = windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation, unsafe.Pointer(&basic), uint32(unsafe.Sizeof(basic)), &size)
	var created, exit, kernel, user windows.Filetime
	if e == nil {
		e = windows.GetProcessTimes(h, &created, &exit, &kernel, &user)
	}
	var image [32768]uint16
	n := uint32(len(image))
	if e == nil {
		e = windows.QueryFullProcessImageName(h, 0, &image[0], &n)
	}
	if e == nil {
		var state uint32
		state, e = windows.WaitForSingleObject(h, 0)
		if state != uint32(windows.WAIT_TIMEOUT) {
			e = errors.New("process exited")
		}
	}
	if e != nil {
		windows.CloseHandle(h)
		return 0, 0, 0, "", e
	}
	return h, uint32(basic.InheritedFromUniqueProcessId), created.Nanoseconds(), windows.UTF16ToString(image[:n]), nil
}
func sshAskpassMain() bool {
	path := os.Getenv(loginPipeEnv)
	if path == "" || len(os.Args) != 2 {
		return false
	}
	answered := false
	var refuse func()
	defer func() {
		if !answered {
			if refuse != nil {
				refuse()
			}
			os.Exit(1)
		}
	}()
	self, parent, _, _, err := loginProcess(uint32(os.Getpid()))
	if err != nil {
		return true
	}
	windows.CloseHandle(self)
	h, _, created, image, err := loginProcess(parent)
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	if !sameLoginImage(image, windowsSSH()) {
		return true
	}
	refuse = func() {
		stop, e := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, parent)
		if e != nil {
			return
		}
		defer windows.CloseHandle(stop)
		var at, exit, kernel, user windows.Filetime
		if windows.GetProcessTimes(stop, &at, &exit, &kernel, &user) == nil && at.Nanoseconds() == created {
			windows.TerminateProcess(stop, 1)
		}
	}
	// Ask SSH to resolve its own effective configuration under the same identity.
	// -G prints configuration and exits without making a network connection.
	var size uint32
	windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, nil, 0, &size)
	if size == 0 || size > 131072 {
		return true
	}
	buffer := make([]byte, size)
	if windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, unsafe.Pointer(&buffer[0]), size, &size) != nil {
		return true
	}
	command := (*windows.NTUnicodeString)(unsafe.Pointer(&buffer[0])).String()
	args, err := windows.DecomposeCommandLine(command)
	if err != nil || len(args) < 2 {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	query := exec.CommandContext(ctx, image, append([]string{"-G"}, args[1:]...)...)
	setupProcess(query)
	configuration := newProcessWindow(128 << 10)
	query.Stdout = configuration
	err = query.Run()
	if err != nil {
		return true
	}
	if configuration.total > 128<<10 {
		return true
	}
	port := 0
	for _, line := range strings.Split(configuration.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "port" {
			port, _ = strconv.Atoi(fields[1])
			break
		}
	}
	if port == 0 {
		return true
	}
	name, _ := windows.UTF16PtrFromString(path)
	var pipe windows.Handle
	for i := 0; i < 100; i++ {
		pipe, err = windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		return true
	}
	file := os.NewFile(uintptr(pipe), "login-pipe")
	defer file.Close()
	var server uint32
	if windows.GetNamedPipeServerProcessId(pipe, &server) != nil {
		return true
	}
	serverHandle, _, _, serverImage, e := loginProcess(server)
	if e != nil {
		return true
	}
	defer windows.CloseHandle(serverHandle)
	selfImage, e := os.Executable()
	if e != nil || !sameLoginImage(serverImage, selfImage) {
		return true
	}
	json.NewEncoder(file).Encode(sshLoginRequest{Prompt: os.Args[1], Port: port})
	n, copyErr := io.Copy(os.Stdout, io.LimitReader(file, 16385))
	answered = n > 0 && (copyErr == nil || errors.Is(copyErr, windows.ERROR_BROKEN_PIPE) || errors.Is(copyErr, windows.ERROR_PIPE_NOT_CONNECTED))
	return true
}
