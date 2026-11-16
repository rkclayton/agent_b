//go:build windows

package modelinstall

import (
	"context"
	"errors"
	"harness/internal/quietproc"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	actualFile, actualErr := os.Stat(actual)
	expectedFile, expectedErr := os.Stat(expected)
	// Windows may report a long image path for a process started through an
	// 8.3 alias. File identity is authoritative; the saved path must stay in root.
	if ok == 0 || !insideRoot(s.Root, s.Executable) || actualErr != nil || expectedErr != nil || !os.SameFile(actualFile, expectedFile) {
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
	if s.StartCommand != "" {
		return launchCommand(s)
	}
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
	if s.StartCommand != "" {
		return findCommand(s)
	}
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

func runStopCommand(s Spec) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := quietproc.Quiet(exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", s.StopCommand))
	cmd.SysProcAttr.CmdLine = `cmd.exe /d /s /c "` + s.StopCommand + `"`
	cmd.Dir = s.Root
	if cmd.Run() != nil {
		return errors.New("stop command failed")
	}
	return nil
}

// Assign the suspended shell before any descendants exist. The retained job,
// not a later PID lookup, is the authority to end the tree we launched.
func launchCommand(s Spec) (*managedProcess, error) {
	shell, err := exec.LookPath("cmd.exe")
	if err != nil {
		return nil, errors.New("command shell unavailable")
	}
	app, _ := syscall.UTF16PtrFromString(shell)
	line, err := syscall.UTF16PtrFromString(`"` + shell + `" /d /s /c "` + s.StartCommand + `"`)
	if err != nil {
		return nil, errors.New("invalid start command")
	}
	dir, err := syscall.UTF16PtrFromString(s.Root)
	if err != nil {
		return nil, errors.New("invalid command directory")
	}
	attributes := syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	startup := syscall.StartupInfo{Cb: uint32(unsafe.Sizeof(syscall.StartupInfo{}))}
	if attributes.HideWindow {
		startup.Flags = 1
		startup.ShowWindow = 0
	}
	var process syscall.ProcessInformation
	if err := syscall.CreateProcess(app, line, nil, nil, false, attributes.CreationFlags|4, nil, dir, &startup, &process); err != nil {
		return nil, errors.New("start command failed")
	}
	defer syscall.CloseHandle(process.Thread)
	defer syscall.CloseHandle(process.Process)
	kernel := syscall.NewLazyDLL("kernel32.dll")
	job, _, _ := kernel.NewProc("CreateJobObjectW").Call(0, 0)
	if job == 0 {
		syscall.TerminateProcess(process.Process, 1)
		return nil, errors.New("command job creation failed")
	}
	assigned, _, _ := kernel.NewProc("AssignProcessToJobObject").Call(job, uintptr(process.Process))
	if assigned == 0 {
		syscall.TerminateProcess(process.Process, 1)
		syscall.CloseHandle(syscall.Handle(job))
		return nil, errors.New("command job assignment failed")
	}
	resumed, _, _ := kernel.NewProc("ResumeThread").Call(uintptr(process.Thread))
	if resumed == 0xffffffff {
		kernel.NewProc("TerminateJobObject").Call(job, 1)
		syscall.CloseHandle(syscall.Handle(job))
		return nil, errors.New("command resume failed")
	}
	closed := false
	exited := func() bool {
		if closed {
			return true
		}
		var stats struct {
			User, Kernel, PeriodUser, PeriodKernel int64
			Faults, Total, Active, Terminated      uint32
		}
		ok, _, _ := kernel.NewProc("QueryInformationJobObject").Call(job, 1, uintptr(unsafe.Pointer(&stats)), unsafe.Sizeof(stats), 0)
		if ok != 0 && stats.Active == 0 {
			syscall.CloseHandle(syscall.Handle(job))
			closed = true
		}
		return closed
	}
	stop := func(command string) error {
		if exited() {
			return nil
		}
		if command != "" {
			copy := s
			copy.StopCommand = command
			if err := runStopCommand(copy); err != nil {
				return err
			}
		} else {
			if ok, _, _ := kernel.NewProc("TerminateJobObject").Call(job, 0); ok == 0 {
				return errors.New("command tree did not stop")
			}
		}
		deadline := time.Now().Add(10 * time.Second)
		for !exited() {
			if time.Now().After(deadline) {
				return errors.New("command tree did not stop")
			}
			time.Sleep(10 * time.Millisecond)
		}
		return nil
	}
	return &managedProcess{exited: exited, stop: func() error { return stop(s.StopCommand) }, commandStop: stop, release: func() {
		if !closed {
			syscall.CloseHandle(syscall.Handle(job))
			closed = true
		}
	}}, nil
}
