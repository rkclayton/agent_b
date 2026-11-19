//go:build windows

package modelinstall

import (
	"context"
	"debug/pe"
	"errors"
	"fmt"
	"harness/internal/quietproc"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// b10964's CPU, Vulkan and CUDA import inventories have the same VC dependency.
func missingDependency(string) bool {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return true
	}
	for _, name := range []string{"msvcp140.dll", "vcruntime140.dll", "vcruntime140_1.dll"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.IsDir() {
			return true
		}
	}
	return false
}
func lockDependencyFile(path string) (func(), error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, err
	}
	return func() { windows.CloseHandle(h) }, nil
}

func runDependency(path string) (uint32, error) {
	// ShellExecute elevates only Microsoft's verified file. The harness token is unchanged.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ole := windows.NewLazySystemDLL("ole32.dll")
	hr, _, _ := ole.NewProc("CoInitializeEx").Call(0, 6)
	if int32(hr) < 0 {
		return 0, errors.New("dependency shell initialization failed")
	}
	defer ole.NewProc("CoUninitialize").Call()
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	args, _ := windows.UTF16PtrFromString("/install /passive /norestart")
	var info struct {
		Size, Mask                        uint32
		Window                            uintptr
		Verb, File, Parameters, Directory *uint16
		Show                              int32
		Instance, List                    uintptr
		Class                             *uint16
		Key                               uintptr
		HotKey                            uint32
		Icon                              uintptr
		Process                           windows.Handle
	}
	info.Size = uint32(unsafe.Sizeof(info))
	info.Mask = 0x40 | 0x100 | 0x400
	info.Verb = verb
	info.File = file
	info.Parameters = args
	info.Show = 1
	ok, _, callErr := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW").Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return 0, callErr
	}
	defer windows.CloseHandle(info.Process)
	if _, err := windows.WaitForSingleObject(info.Process, windows.INFINITE); err != nil {
		return 0, err
	}
	var exit uint32
	err = windows.GetExitCodeProcess(info.Process, &exit)
	return exit, err
}

var runtimeProbeMode sync.Mutex

func probeRuntime(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := quietproc.Quiet(exec.CommandContext(ctx, path, "--version"))
	cmd.Dir = filepath.Dir(path)
	// A missing import must produce an exit status, never a loader or WER dialog.
	runtimeProbeMode.Lock()
	mode := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetErrorMode")
	previous, _, _ := mode.Call(0x8003)
	err := cmd.Start()
	mode.Call(previous)
	runtimeProbeMode.Unlock()
	if err == nil {
		err = cmd.Wait()
	}
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return errors.New("Runtime exit: timeout (15 s).")
	}
	code := uint32(0xffffffff)
	if cmd.ProcessState != nil {
		code = uint32(cmd.ProcessState.ExitCode())
	} else {
		var systemError syscall.Errno
		if errors.As(err, &systemError) {
			code = uint32(systemError)
		}
	}
	line := fmt.Sprintf("Runtime exit: 0x%08X.", code)
	if code == 0xc0000135 {
		if p, e := pe.Open(path); e == nil {
			defer p.Close()
			symbols, _ := p.ImportedSymbols()
			system, _ := windows.GetSystemDirectory()
			for _, s := range symbols {
				parts := strings.Split(s, ":")
				dll := parts[len(parts)-1]
				if filepath.Base(dll) != dll || strings.HasPrefix(strings.ToLower(dll), "api-ms-") {
					continue
				}
				_, local := os.Stat(filepath.Join(filepath.Dir(path), dll))
				_, global := os.Stat(filepath.Join(system, dll))
				if local != nil && global != nil {
					line += " Missing DLL: " + dll + "."
					break
				}
			}
		}
	}
	return errors.New(line)
}

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
