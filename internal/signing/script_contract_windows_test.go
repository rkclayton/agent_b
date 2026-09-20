//go:build windows

package signing

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPowerShellSigningScriptsUseHostCompatibleCodeSigningEKUCheck(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	for _, relative := range []string{"scripts/manage-signing.ps1", "scripts/install-Agent_b.ps1"} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, "([string]$_.ObjectId) -eq '1.3.6.1.5.5.7.3.3'") {
			t.Errorf("%s does not accept PowerShell's string-valued EnhancedKeyUsageList.ObjectId", relative)
		}
		if strings.Contains(text, "$_.ObjectId.Value -eq '1.3.6.1.5.5.7.3.3'") {
			t.Errorf("%s retains the host-incompatible ObjectId.Value check", relative)
		}
	}
	manager, err := os.ReadFile(filepath.Join(root, "scripts", "manage-signing.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(manager)
	for _, required := range []string{"Cert:\\LocalMachine\\My", "-KeyExportPolicy NonExportable", "-Verb RunAs"} {
		if !strings.Contains(text, required) {
			t.Errorf("manage-signing.ps1 does not preserve %q", required)
		}
	}
	if strings.Contains(text, "-KeyProtection ProtectHigh") {
		t.Error("self-created signing keys must be UAC-gated rather than password-prompted per signature")
	}
	installer, err := os.ReadFile(filepath.Join(root, "scripts", "install-Agent_b.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installer), "$SigningThumbprint -eq 'auto'") {
		t.Error("installer does not support first-install certificate bootstrap inside its existing elevation")
	}
	if !strings.Contains(string(installer), "-not $TestMode -and -not $SigningThumbprint") {
		t.Error("isolated TestMode installs must not use or create the production signing key")
	}
	for _, required := range []string{
		"Get-ChildItem -LiteralPath 'Cert:\\LocalMachine\\My'",
		"REUSED: administrator-gated signing certificate",
		"Add-CurrentUserCertificate -Certificate $certificate -StoreName TrustedPublisher",
		"Add-CurrentUserCertificate -Certificate $certificate -StoreName Root",
		"$PSVersionTable.PSEdition -ne 'Desktop'",
		"Assert-CandidateIdentity -SourceRoot $sourceRoot -Binary $sourceBinary -Version $displayVersion",
	} {
		if !strings.Contains(string(installer), required) {
			t.Errorf("installer does not preserve seamless bootstrap contract %q", required)
		}
	}
	// Item 2eu: the installer signs what the release step built and never builds.
	if strings.Contains(string(installer), "go build") || strings.Contains(string(installer), "Find-Go") {
		t.Error("installer builds; the release step builds the candidate and the installer only verifies it")
	}
	if strings.Contains(string(installer), "Import-Certificate -FilePath $tempCertificate") {
		t.Error("installer retains interactive certificate import path")
	}
}

// Item 2gc, v1.1.1/W1. WALK-3 found `GET /api/signing` answering 500 because
// manage-signing.ps1 ran `whoami.exe /groups` by bare name and a PATH that
// reached Git's POSIX whoami first answered with an error. Windows' own
// utilities are resolved by absolute path now, and the group check reads the
// token directly.
func TestTheSigningScriptsResolveWindowsUtilitiesByPath(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	scripts, err := filepath.Glob(filepath.Join(root, "scripts", "*.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	// Every bare-name invocation of a utility that ships with Windows. A
	// developer toolchain (git, node, go) stays PATH-resolved on purpose.
	bare := []string{"& whoami", "& icacls.exe", "& icacls ", "& powershell.exe", "& cmd.exe", "& netsh", "& schtasks", "& takeown", "& robocopy.exe", "& certutil", "& reg.exe", "& sc.exe"}
	for _, path := range scripts {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(path)
		for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			for _, form := range bare {
				if strings.Contains(trimmed, form) {
					t.Errorf("%s resolves a Windows utility by bare name: %s", name, trimmed)
				}
			}
		}
	}
	signing, err := os.ReadFile(filepath.Join(root, "scripts", "manage-signing.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(signing), "Get-WindowsTool 'whoami.exe'") {
		t.Error("manage-signing.ps1 does not resolve whoami by absolute path")
	}
	if strings.Contains(string(signing), "& whoami") {
		t.Error("manage-signing.ps1 still invokes whoami by bare name")
	}
	if !strings.Contains(string(signing), "unsupported =") {
		t.Error("manage-signing.ps1 does not answer a failed group check as unsupported")
	}
	helper, err := os.ReadFile(filepath.Join(root, "scripts", "windows-tools.ps1"))
	if err != nil {
		t.Fatalf("the shared resolver is missing: %v", err)
	}
	for _, want := range []string{"function Get-WindowsTool", "function Get-WindowsPowerShell", "Sysnative"} {
		if !strings.Contains(string(helper), want) {
			t.Errorf("windows-tools.ps1 has no %s", want)
		}
	}
}

// The live proof: with Git's usr/bin first on PATH — the exact condition the
// walk hit — the signing status still answers.
func TestTheSigningStatusAnswersWithAShadowingPathFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode runs no script")
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	shadow := ""
	for _, candidate := range []string{`C:\Program Files\Git\usr\bin`, `C:\Program Files (x86)\Git\usr\bin`} {
		if info, err := os.Stat(filepath.Join(candidate, "whoami.exe")); err == nil && info.Mode().IsRegular() {
			shadow = candidate
			break
		}
	}
	if shadow == "" {
		t.Skip("this host has no POSIX whoami.exe to shadow with")
	}
	t.Setenv("PATH", shadow+string(os.PathListSeparator)+os.Getenv("PATH"))
	manager := New(filepath.Join(root, "scripts", "manage-signing.ps1"))
	status, err := manager.Status(t.Context(), Request{ProcessID: os.Getpid()})
	if err != nil {
		t.Fatalf("the signing status failed with a shadowing PATH: %v", err)
	}
	// CanManage is a fact about the account, not about PATH; the point is that
	// an answer came back at all.
	t.Logf("status with %s first on PATH: can_manage=%t supported=%t", shadow, status.CanManage, status.Supported)
}
