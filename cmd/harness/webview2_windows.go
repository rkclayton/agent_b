//go:build windows

package main

// Item 2hc (v1.3.0/W2): the WebView2 loader, bound the narrow way.
//
// WHY THIS FILE EXISTS AT ALL. Three orders tried to get the window's top edge
// from Edge and none did: the Window Controls Overlay measures 0x0 in an
// ordinary window, under --app=, and (the operator measured it elevated on
// 2026-09-21) on a policy-installed PWA too. So the frame becomes ours.
//
// WHERE THE LOADER COMES FROM. The WebView2 runtime does NOT ship
// WebView2Loader.dll - measured in v1.3.0/W1, it ships EmbeddedBrowserWebView
// .dll and nothing else - so every WebView2 application carries its own copy.
// Ours is Microsoft's, from their SDK package, pinned by SHA-256 AND
// Authenticode signature in scripts/webview2-loader.json and verified by the
// installer before it is copied.
//
// WHAT THIS DELIBERATELY DOES NOT DO. It does not embed the DLL and map it into
// executable memory, which is what github.com/jchv/go-webview2 does and why
// that module was refused (NOTES.md, 2026-09-22). It does not search the
// process's DLL path: the loader is taken from the executable's OWN directory
// by absolute path and nowhere else, because a DLL beside an executable is the
// file an attacker would most want to plant.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

const webview2LoaderName = "WebView2Loader.dll"

var (
	// SUCCESS is cached, failure is not. A probe that ran before the loader was
	// in place must not poison the process for the rest of its life; retrying
	// costs one Stat on a path we already hold.
	webview2Mutex  sync.Mutex
	webview2Module *syscall.DLL

	procCreateEnvironment *syscall.Proc
	procAvailableVersion  *syscall.Proc
)

// webview2LoaderPath is the loader beside this executable, and only there.
// LoadLibrary with a bare name would search the working directory and the PATH;
// an absolute path in the application directory is the whole point.
func webview2LoaderPath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating the executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		resolved = executable
	}
	return filepath.Join(filepath.Dir(resolved), webview2LoaderName), nil
}

// loadWebView2 loads the pinned loader once. A missing or unloadable loader is
// an ordinary answer, not a failure of the product: the caller falls back to the
// browser window and says so in the log.
func loadWebView2() error {
	webview2Mutex.Lock()
	defer webview2Mutex.Unlock()
	if webview2Module != nil {
		return nil
	}
	path, err := webview2LoaderPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%s is not beside the executable: %w", webview2LoaderName, err)
	}
	module, err := syscall.LoadDLL(path)
	if err != nil {
		return fmt.Errorf("loading %s: %w", path, err)
	}
	create, err := module.FindProc("CreateCoreWebView2EnvironmentWithOptions")
	if err != nil {
		return fmt.Errorf("%s has no CreateCoreWebView2EnvironmentWithOptions: %w", webview2LoaderName, err)
	}
	available, err := module.FindProc("GetAvailableCoreWebView2BrowserVersionString")
	if err != nil {
		return fmt.Errorf("%s has no GetAvailableCoreWebView2BrowserVersionString: %w", webview2LoaderName, err)
	}
	webview2Module, procCreateEnvironment, procAvailableVersion = module, create, available
	return nil
}

// webview2RuntimeVersion asks the loader which runtime is installed. An empty
// version with no error means the loader answered and there is no runtime,
// which is exactly the honest "absent" the fallback needs; the registry is not
// consulted, because the loader's answer is the one that governs whether an
// environment can actually be created.
func webview2RuntimeVersion() (string, error) {
	if err := loadWebView2(); err != nil {
		return "", err
	}
	var version *uint16
	result, _, _ := procAvailableVersion.Call(0, uintptr(unsafe.Pointer(&version)))
	if version == nil {
		if result != 0 {
			return "", fmt.Errorf("the WebView2 runtime is not installed (0x%X)", uint32(result))
		}
		return "", nil
	}
	defer coTaskMemFree(uintptr(unsafe.Pointer(version)))
	if result != 0 {
		return "", fmt.Errorf("querying the WebView2 runtime failed (0x%X)", uint32(result))
	}
	return utf16PtrToString(version), nil
}

// hostWindowAvailable is the probe the launcher and startup use: it reports the
// runtime version when a host window could be opened, and the reason it cannot
// otherwise. Both outcomes are logged by the caller; neither is an error state
// for the product, because the browser path remains supported.
func hostWindowAvailable() (string, error) {
	version, err := webview2RuntimeVersion()
	if err != nil {
		return "", err
	}
	if version == "" {
		return "", fmt.Errorf("the WebView2 runtime is not installed")
	}
	return version, nil
}

var (
	ole32              = syscall.NewLazyDLL("ole32.dll")
	procCoTaskMemFree  = ole32.NewProc("CoTaskMemFree")
	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procCoUninitialize = ole32.NewProc("CoUninitialize")
)

func coTaskMemFree(p uintptr) {
	if p != 0 {
		procCoTaskMemFree.Call(p)
	}
}

func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	var runes []uint16
	for offset := uintptr(0); ; offset += 2 {
		unit := *(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + offset))
		if unit == 0 {
			break
		}
		runes = append(runes, unit)
	}
	return syscall.UTF16ToString(runes)
}
