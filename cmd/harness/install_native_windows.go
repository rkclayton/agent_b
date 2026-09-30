//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"harness/internal/nativepolicy"
	"harness/internal/quietproc"
)

func runNativePerUserInstall(source string, arguments []string, dataRoot string, log *installLog) error {
	if err := verifyNativeCandidate(source); err != nil {
		return err
	}
	local := installerArgument(arguments, "OperatorLocalAppData", os.Getenv("LOCALAPPDATA"))
	expectedApplication := filepath.Join(local, "Programs", "Agent_b")
	expectedData := filepath.Join(local, "Agent_b")
	expectedWorkspace := filepath.Join(local, "Agent_b-workspace")
	application := installerArgument(arguments, "ApplicationDirectory", expectedApplication)
	data := installerArgument(arguments, "DataDirectory", expectedData)
	workspace := installerArgument(arguments, "WorkspaceDirectory", expectedWorkspace)
	nativeTestMode := installerFlagPresent(arguments, "NativeTestMode")
	if !nativeTestMode {
		if !strings.EqualFold(filepath.Clean(application), filepath.Clean(expectedApplication)) {
			return fmt.Errorf("ApplicationDirectory must be the canonical per-user LocalAppData location: %s", expectedApplication)
		}
		if !strings.EqualFold(filepath.Clean(data), filepath.Clean(expectedData)) {
			return fmt.Errorf("DataDirectory must be the launching operator's LocalAppData Agent_b directory: %s", expectedData)
		}
		if !strings.EqualFold(filepath.Clean(workspace), filepath.Clean(expectedWorkspace)) {
			return fmt.Errorf("WorkspaceDirectory must be the canonical per-user LocalAppData location: %s", expectedWorkspace)
		}
	}
	operatorSID, err := installOperatorSID(currentTokenSID, func() (string, error) { return "", fmt.Errorf("name lookup is forbidden") })
	if err != nil {
		return fmt.Errorf("read process-token user SID: %w", err)
	}
	if supplied := installerArgument(arguments, "OperatorSid", ""); supplied != "" && !strings.EqualFold(supplied, operatorSID) {
		return fmt.Errorf("installation refused: operator SID %s differs from process token %s", supplied, operatorSID)
	}
	if err := stopNativeInstalledProcess(application, data, log); err != nil {
		return err
	}
	version := strings.TrimPrefix(currentDisplayVersion(source), "v")
	plan := nativeInstallPlan{Source: source, Application: application, Data: data, Workspace: workspace,
		StartMenu: installerArgument(arguments, "StartMenuDirectory", filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs")),
		SendTo:    installerArgument(arguments, "SendToDirectory", filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "SendTo")),
		Registry:  installerArgument(arguments, "UninstallRegistryPath", `HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Agent_b`),
		Version:   version, OperatorSID: operatorSID}
	if nativeTestMode && !strings.HasPrefix(strings.ToLower(plan.Registry), `hkcu:\software\agent_b-installer-test-`) {
		return fmt.Errorf("NativeTestMode registry must be beneath HKCU:\\Software\\Agent_b-Installer-Test-*")
	}
	platform := nativeInstallPlatform{shortcut: writeShellLink, register: func(values map[string]any) error { return writeUninstallRegistration(plan.Registry, values) }, secure: secureInstallDirectory}
	policy := effectiveExecutionPolicy()
	log.printf("execution policy: %s from %s", policy.Policy, policy.Scope)
	policyPath := filepath.Join(data, "execution-policy.txt")
	if policy.BlocksScripts {
		message := fmt.Sprintf("Windows policy on this machine disables PowerShell scripts (%s, set by %s); the service identity cannot be set up here", policy.Policy, policy.Scope)
		log.printf("%s", message)
		if err := os.MkdirAll(data, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(policyPath, []byte(message+"\n"), 0o600); err != nil {
			return fmt.Errorf("record execution policy: %w", err)
		}
	} else if err := os.Remove(policyPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear execution policy record: %w", err)
	}
	appendProgress(data, installProgress{Phase: "copying the application", Text: "Application: " + application})
	if err := installPerUserNative(plan, platform); err != nil {
		return err
	}
	log.printf("INSTALLATION COMPLETE")
	return nil
}

func effectiveExecutionPolicy() executionPolicyState {
	values := map[string]string{"Process": os.Getenv("PSExecutionPolicyPreference")}
	for scope, item := range map[string]struct {
		root registry.Key
		path string
		gpo  bool
	}{
		"MachinePolicy": {registry.LOCAL_MACHINE, `SOFTWARE\Policies\Microsoft\Windows\PowerShell`, true},
		"UserPolicy":    {registry.CURRENT_USER, `SOFTWARE\Policies\Microsoft\Windows\PowerShell`, true},
		"CurrentUser":   {registry.CURRENT_USER, `SOFTWARE\Microsoft\PowerShell\1\ShellIds\Microsoft.PowerShell`, false},
		"LocalMachine":  {registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\PowerShell\1\ShellIds\Microsoft.PowerShell`, false},
	} {
		key, err := registry.OpenKey(item.root, item.path, registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err != nil {
			continue
		}
		if item.gpo {
			if enabled, _, err := key.GetIntegerValue("EnableScripts"); err == nil && enabled == 0 {
				values[scope] = "Restricted"
			} else if err == nil {
				values[scope], _, _ = key.GetStringValue("ExecutionPolicy")
			}
		} else {
			values[scope], _, _ = key.GetStringValue("ExecutionPolicy")
		}
		key.Close()
	}
	return resolveExecutionPolicy(values)
}
func currentTokenSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}
func secureInstallDirectory(path, sid string, _ bool) error {
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
func writeUninstallRegistration(path string, values map[string]any) error {
	path = strings.TrimPrefix(strings.TrimPrefix(path, `HKCU:\`), `HKEY_CURRENT_USER\`)
	key, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	for name, value := range values {
		switch typed := value.(type) {
		case string:
			if err := key.SetStringValue(name, typed); err != nil {
				return err
			}
		case uint32:
			if err := key.SetDWordValue(name, typed); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported registry value %s", name)
		}
	}
	return nil
}

type comInterface struct{ vtable **uintptr }

func comMethod(object *comInterface, index uintptr) uintptr {
	return *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(object.vtable)) + index*unsafe.Sizeof(uintptr(0))))
}
func failedHRESULT(value uintptr) bool { return int32(value) < 0 }
func writeShellLink(spec shortcutSpec) error {
	if err := os.MkdirAll(filepath.Dir(spec.Path), 0o700); err != nil {
		return err
	}
	ole32 := windows.NewLazySystemDLL("ole32.dll")
	coInitialize := ole32.NewProc("CoInitializeEx")
	coCreate := ole32.NewProc("CoCreateInstance")
	coUninitialize := ole32.NewProc("CoUninitialize")
	const coinitApartmentThreaded, clsctxInprocServer = 2, 1
	initialized, _, _ := coInitialize.Call(0, coinitApartmentThreaded)
	if failedHRESULT(initialized) && uint32(initialized) != 0x80010106 {
		return syscall.Errno(initialized)
	}
	if !failedHRESULT(initialized) {
		defer coUninitialize.Call()
	}
	classID := windows.GUID{Data1: 0x00021401, Data2: 0, Data3: 0, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	linkID := windows.GUID{Data1: 0x000214f9, Data2: 0, Data3: 0, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	var link *comInterface
	hresult, _, _ := coCreate.Call(uintptr(unsafe.Pointer(&classID)), 0, clsctxInprocServer, uintptr(unsafe.Pointer(&linkID)), uintptr(unsafe.Pointer(&link)))
	if failedHRESULT(hresult) {
		return fmt.Errorf("create shell link: HRESULT 0x%x", hresult)
	}
	defer syscall.SyscallN(comMethod(link, 2), uintptr(unsafe.Pointer(link)))
	callString := func(index uintptr, value string) error {
		pointer, err := windows.UTF16PtrFromString(value)
		if err != nil {
			return err
		}
		result, _, _ := syscall.SyscallN(comMethod(link, index), uintptr(unsafe.Pointer(link)), uintptr(unsafe.Pointer(pointer)))
		if failedHRESULT(result) {
			return fmt.Errorf("shell-link property: HRESULT 0x%x", result)
		}
		return nil
	}
	for index, value := range map[uintptr]string{20: spec.Target, 11: spec.Arguments, 9: spec.WorkingDirectory, 7: spec.Description} {
		if err := callString(index, value); err != nil {
			return err
		}
	}
	icon, err := windows.UTF16PtrFromString(strings.TrimSuffix(spec.Icon, ",0"))
	if err != nil {
		return err
	}
	if result, _, _ := syscall.SyscallN(comMethod(link, 17), uintptr(unsafe.Pointer(link)), uintptr(unsafe.Pointer(icon)), 0); failedHRESULT(result) {
		return fmt.Errorf("shell-link icon: HRESULT 0x%x", result)
	}
	persistID := windows.GUID{Data1: 0x0000010b, Data2: 0, Data3: 0, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	var persist *comInterface
	if result, _, _ := syscall.SyscallN(comMethod(link, 0), uintptr(unsafe.Pointer(link)), uintptr(unsafe.Pointer(&persistID)), uintptr(unsafe.Pointer(&persist))); failedHRESULT(result) {
		return fmt.Errorf("query IPersistFile: HRESULT 0x%x", result)
	}
	defer syscall.SyscallN(comMethod(persist, 2), uintptr(unsafe.Pointer(persist)))
	path, err := windows.UTF16PtrFromString(spec.Path)
	if err != nil {
		return err
	}
	if result, _, _ := syscall.SyscallN(comMethod(persist, 6), uintptr(unsafe.Pointer(persist)), uintptr(unsafe.Pointer(path)), 1); failedHRESULT(result) {
		return fmt.Errorf("save shell link: HRESULT 0x%x", result)
	}
	return nil
}
func stopNativeInstalledProcess(application, data string, log *installLog) error {
	encoded, err := readMarker(filepath.Join(data, "agent_b-run.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var marker runMarker
	if json.Unmarshal(encoded, &marker) != nil || marker.PID <= 0 || !processRunning(marker.PID, marker.Created) {
		return nil
	}
	want, _ := filepath.Abs(application)
	got, _ := filepath.Abs(marker.Application)
	if !strings.EqualFold(filepath.Clean(want), filepath.Clean(got)) {
		return nil
	}
	name, _ := windows.UTF16PtrFromString(stopEventName(application, marker.PID))
	event, _, _ := procOpenEvent.Call(0x0002, 0, uintptr(unsafe.Pointer(name)))
	if event == 0 {
		return fmt.Errorf("Agent_b PID %d could not be stopped through its scoped event; installation was not changed", marker.PID)
	}
	defer windows.CloseHandle(windows.Handle(event))
	if ok, _, _ := procSetEvent.Call(event); ok == 0 {
		return fmt.Errorf("signal Agent_b PID %d", marker.PID)
	}
	log.printf("STOP REQUESTED: Agent_b PID %d through the scoped event", marker.PID)
	deadline := time.Now().Add(15 * time.Second)
	for processRunning(marker.PID, marker.Created) && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if processRunning(marker.PID, marker.Created) {
		return fmt.Errorf("Agent_b PID %d did not exit after the graceful stop signal; installation was not changed", marker.PID)
	}
	return nil
}

func runNativeUninstall(application, data, startMenu, sendTo, uninstallRegistry string, purge, worker bool, parent int) error {
	local := os.Getenv("LOCALAPPDATA")
	if strings.TrimSpace(application) == "" {
		application = filepath.Join(local, "Programs", "Agent_b")
	}
	if strings.TrimSpace(data) == "" {
		data = filepath.Join(local, "Agent_b")
	}
	if strings.TrimSpace(startMenu) == "" {
		startMenu = filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs")
	}
	if strings.TrimSpace(sendTo) == "" {
		sendTo = filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "SendTo")
	}
	if strings.TrimSpace(uninstallRegistry) == "" {
		uninstallRegistry = `HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Agent_b`
	}
	plan := nativeInstallPlan{Application: application, Data: data, StartMenu: startMenu, SendTo: sendTo, Registry: uninstallRegistry}
	if !worker {
		log := openInstallLog(data, true)
		defer log.close()
		if err := stopNativeInstalledProcess(application, data, log); err != nil {
			return err
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		helper := filepath.Join(os.TempDir(), fmt.Sprintf("Agent_b-uninstall-%d.exe", os.Getpid()))
		if err := copyNativeFile(self, helper); err != nil {
			return err
		}
		arguments := []string{"--uninstall-worker", "--uninstall-parent", fmt.Sprint(os.Getpid()), "--app-root", application, "--data-root", data}
		arguments = append(arguments, "--start-menu-root", startMenu, "--send-to-root", sendTo, "--uninstall-registry-path", uninstallRegistry)
		if purge {
			arguments = append(arguments, "--purge-data")
		}
		command := exec.Command(helper, arguments...)
		quietproc.Quiet(command)
		detachChild(command)
		return command.Start()
	}
	if parent > 0 {
		if handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(parent)); err == nil {
			_, _ = windows.WaitForSingleObject(handle, 30000)
			windows.CloseHandle(handle)
		}
	}
	return uninstallPerUserNative(plan, purge, func() error {
		path := strings.TrimPrefix(strings.TrimPrefix(uninstallRegistry, `HKCU:\`), `HKEY_CURRENT_USER\`)
		err := registry.DeleteKey(registry.CURRENT_USER, path)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	})
}

func runServiceHelper(requestPath, resultPath string) error {
	encoded, err := os.ReadFile(requestPath)
	if err != nil {
		return err
	}
	var request nativepolicy.HelperRequest
	if err := json.Unmarshal(encoded, &request); err != nil {
		return err
	}
	result := nativepolicy.ExecuteHelper(request)
	encoded, _ = json.Marshal(result)
	temporary := resultPath + ".writing"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, resultPath); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("%s", result.Message)
	}
	return nil
}
