//go:build windows

package modelinstall

import (
	"errors"
	"harness/internal/quietproc"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

func startDetached(path string, args []string, directory string) (int, error) {
	command := quietproc.Quiet(exec.Command(path, args...))
	command.Dir = directory
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
	if err := command.Process.Release(); err != nil {
		return 0, err
	}
	return pid, nil
}

func claimManaged(pid int, s Spec) (*managedProcess, error) {
	h, err := syscall.OpenProcess(0x1000|0x00100000|1, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	query := syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	ok, _, callErr := query.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	actual := syscall.UTF16ToString(buf[:size])
	expected, _ := filepath.EvalSymlinks(s.Executable)
	if ok == 0 || !insideRoot(s.Root, actual) || !strings.EqualFold(actual, expected) {
		syscall.CloseHandle(h)
		if ok == 0 {
			return nil, callErr
		}
		return nil, errors.New("installed executable ownership could not be proved")
	}
	closed := false
	exited := func() bool {
		if closed {
			return true
		}
		result, _ := syscall.WaitForSingleObject(h, 0)
		if result == syscall.WAIT_OBJECT_0 {
			syscall.CloseHandle(h)
			closed = true
			return true
		}
		return false
	}
	return &managedProcess{exited: exited, stop: func() error {
		if exited() {
			return nil
		}
		if err := syscall.TerminateProcess(h, 0); err != nil {
			return err
		}
		result, err := syscall.WaitForSingleObject(h, 10000)
		if err != nil {
			return err
		}
		if result != syscall.WAIT_OBJECT_0 {
			return errors.New("model process did not exit")
		}
		syscall.CloseHandle(h)
		closed = true
		return nil
	}}, nil
}
func claimInstalled(pid int, s Spec) (*managedProcess, error) { return claimManaged(pid, s) }
func launchManaged(s Spec) (*managedProcess, error) {
	if !insideRoot(s.Root, s.Executable) {
		return nil, errors.New("installed executable ownership could not be proved")
	}
	cmd := quietproc.Quiet(exec.Command(s.Executable, s.Args...))
	cmd.Dir = s.Root
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p, err := claimManaged(cmd.Process.Pid, s)
	if err != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Process.Release()
	return p, err
}
func findManaged(s Spec) (*managedProcess, error) {
	u, err := url.Parse(s.URL)
	if err != nil || u.Port() == "" {
		return nil, errors.New("saved local port is missing")
	}
	out, err := quietproc.Quiet(exec.Command("netstat.exe", "-ano", "-p", "TCP")).Output()
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 5 && f[0] == "TCP" && strings.HasSuffix(f[1], ":"+u.Port()) && f[3] == "LISTENING" {
			pid, err := strconv.Atoi(f[4])
			if err != nil {
				return nil, err
			}
			return claimManaged(pid, s)
		}
	}
	return nil, nil
}
