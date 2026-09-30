package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"harness/internal/buildinfo"
	"harness/internal/quietproc"
)

type installOptions struct {
	quiet         bool
	sourceDir     string
	dataRoot      string
	noStart       bool
	allUsers      bool
	reopenSession string
	passThough    []string
}

func installOperatorSID(tokenUser func() (string, error), _ func() (string, error)) (string, error) {
	return tokenUser()
}

type installProgress struct {
	At    string `json:"at"`
	Phase string `json:"phase"`
	Text  string `json:"text"`
	Done  bool   `json:"done,omitempty"`
	OK    bool   `json:"ok,omitempty"`
}

const installProgressName = "install-progress.jsonl"

var installBundleMagic = []byte("AGENTBUNDLE0001!")

const installBundleFooterSize = 16 + 8 + sha256.Size

func installProgressPath(dataRoot string) string {
	return filepath.Join(dataRoot, installProgressName)
}

func appendProgress(dataRoot string, entry installProgress) {
	entry.At = time.Now().UTC().Format(time.RFC3339)
	encoded, err := json.Marshal(entry)
	if err != nil {
		return
	}
	file, err := os.OpenFile(installProgressPath(dataRoot), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(encoded, '\n'))
}

func phaseFor(line string) string {
	switch {
	case strings.HasPrefix(line, "PREFLIGHT"):
		return "preflight"
	case strings.HasPrefix(line, "CANDIDATE:"):
		return "checking the candidate"
	case strings.HasPrefix(line, "STOPPING") || strings.HasPrefix(line, "STOPPED"):
		return "stopping the running application"
	case strings.HasPrefix(line, "CREATED") || strings.HasPrefix(line, "Application:"):
		return "copying the application"
	case strings.HasPrefix(line, "INSTALLATION COMPLETE"):
		return "finishing"
	case strings.HasPrefix(line, "INSTALLATION FAILED"):
		return "failed"
	}
	return ""
}

func windowsPowerShell() string {
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

func runInstall(options installOptions, args []string) int {
	dataRoot := installDataRoot(options.dataRoot, args)
	log := openInstallLog(dataRoot, options.quiet)
	defer log.close()
	installDone := make(chan struct{})
	log.printf("install: starting; data root %s", dataRoot)

	source := options.sourceDir
	var removeSource func()
	embeddedBundle := false
	if source == "" {
		executable, err := os.Executable()
		if err != nil {
			return log.fail("cannot locate this executable: %v", err)
		}
		source = filepath.Dir(executable)
		if embedded, cleanup, found, extractErr := extractInstallBundle(executable); extractErr != nil {
			return log.fail("embedded installer payload is invalid: %v", extractErr)
		} else if found {
			source, removeSource = embedded, cleanup
			embeddedBundle = true
			defer removeSource()
			log.printf("install: verified and extracted the embedded application payload")
		}
	}
	log.printf("install: source %s", source)
	script := filepath.Join(source, "scripts", "install-Agent_b.ps1")
	if _, err := os.Stat(script); err != nil {
		return log.fail("%s is missing; run this from the candidate folder", script)
	}

	marker := InstallMarker{Phase: "starting", Version: currentDisplayVersion(source), Source: source, Quiet: options.quiet}
	if err := writeInstallMarker(dataRoot, marker); err != nil {
		return log.fail("could not record that the install began: %v", err)
	}
	_ = os.Remove(installProgressPath(dataRoot))
	appendProgress(dataRoot, installProgress{Phase: "starting", Text: "Installing Agent_b " + marker.Version})

	var waitInstaller func() error
	nativeInstall := !options.allUsers && !installerFlagPresent(args, "WhatIf") && (!installerFlagPresent(args, "TestMode") || installerFlagPresent(args, "NativeTestMode"))
	if nativeInstall {
		waitInstaller = func() error {
			err := runNativePerUserInstall(source, args, dataRoot, log)
			if err != nil {
				log.printf("INSTALLATION FAILED: %v", err)
			}
			return err
		}
		log.printf("install: native per-user installer selected")
	} else {
		powershell := windowsPowerShell()
		scriptArgs := []string{"-NoLogo", "-NoProfile", "-File", script, "-ProgressFile", installProgressPath(dataRoot)}
		if options.allUsers {
			scriptArgs = append(scriptArgs, "-AllUsers")
		}
		if embeddedBundle {
			scriptArgs = append(scriptArgs, "-EmbeddedBundle")
		}
		scriptArgs = append(scriptArgs, args...)
		command := exec.Command(powershell, scriptArgs...)
		quietproc.Quiet(command)
		command.Dir = source
		for _, variable := range os.Environ() {
			if strings.EqualFold(strings.SplitN(variable, "=", 2)[0], "PSModulePath") {
				continue
			}
			command.Env = append(command.Env, variable)
		}
		command.Stdout, command.Stderr = log.writer(), log.writer()
		detachChild(command)
		if err := command.Start(); err != nil {
			return log.fail("could not start the installer: %v", err)
		}
		log.printf("install: the elevated installer is running as PID %d; it does not depend on this window", command.Process.Pid)
		waitInstaller = command.Wait
	}

	lastPhase := "starting"
	followed := make(chan struct{})
	go func() {
		defer close(followed)
		for {
			entries, err := readInstallProgress(dataRoot)
			if err == nil && len(entries) > 0 {
				if phase := entries[len(entries)-1].Phase; phase != "" && phase != lastPhase {
					lastPhase = phase
					marker.Phase = phase
					_ = writeInstallMarker(dataRoot, marker)
				}
			}
			select {
			case <-time.After(250 * time.Millisecond):
			case <-installDone:
				return
			}
		}
	}()
	waitErr := waitInstaller()
	close(installDone)
	<-followed
	code := 0
	if waitErr != nil {
		code = 1
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code = exitErr.ExitCode()
		}
	}

	finish := installProgress{Phase: "finished", Text: "Agent_b " + marker.Version + " is installed.", Done: true, OK: true}
	if code == 0 {
		if err := clearInstallMarker(dataRoot); err != nil {
			log.printf("install: the install finished but its marker could not be cleared: %v", err)
		}
		if options.noStart {
			appendProgress(dataRoot, finish)
			log.printf("AUTOSTART SKIPPED: -NoStart was requested. Log: %s", log.location())
			return 0
		}
		appendProgress(dataRoot, installProgress{Phase: "restarting", Text: "Starting Agent_b " + marker.Version})
		applicationRoot := installerArgument(args, "ApplicationDirectory", defaultInstallRoot(options.allUsers))
		operatorDataRoot := installerArgument(args, "DataDirectory", filepath.Join(os.Getenv("LOCALAPPDATA"), "Agent_b"))
		if err := launchInstalledAgent(applicationRoot, operatorDataRoot, options.reopenSession, nativeInstall, log); err != nil {
			appendProgress(dataRoot, installProgress{Phase: "restarting", Text: fmt.Sprintf("Agent_b %s was installed but failed to start: %v. Transcript: %s", marker.Version, err, log.location()), Done: true})
			return log.fail("Agent_b was installed but failed to start: %v", err)
		}
		leftInPlace, err := completeInstallMigration(applicationRoot, operatorDataRoot, installerFlagPresent(args, "TestMode"), log)
		if err != nil {
			appendProgress(dataRoot, installProgress{Phase: "finished", Text: fmt.Sprintf("Agent_b %s started, but legacy migration cleanup failed: %v. Transcript: %s", marker.Version, err, log.location()), Done: true})
			return log.fail("Agent_b started, but legacy migration cleanup failed: %v", err)
		}
		if leftInPlace != "" {
			log.printf("%s", leftInPlace)
			appendProgress(dataRoot, installProgress{Phase: "warning", Text: leftInPlace, OK: true})
		}
		appendProgress(dataRoot, finish)
		if nativeInstall {
			log.printf("AUTOSTART COMPLETE: Agent_b started natively from %s. Log: %s", filepath.Join(applicationRoot, "Agent_b.exe"), log.location())
		} else {
			log.printf("AUTOSTART COMPLETE: Agent_b started through %s. Log: %s", filepath.Join(applicationRoot, "scripts", "launch-Agent_b.ps1"), log.location())
		}
		return 0
	}
	if version, reason, restart := installRestartDetails(log.location()); restart {
		applicationRoot := installerArgument(args, "ApplicationDirectory", defaultInstallRoot(options.allUsers))
		operatorDataRoot := installerArgument(args, "DataDirectory", filepath.Join(os.Getenv("LOCALAPPDATA"), "Agent_b"))
		if err := launchInstalledAgent(applicationRoot, operatorDataRoot, options.reopenSession, nativeInstall, log); err != nil {
			log.printf("RESTART FAILED: %s after %s: %v", version, reason, err)
		} else {
			log.printf("RESTARTED: %s after %s.", version, reason)
		}
	}
	failureText := fmt.Sprintf("The install stopped during %s (exit %d). It is safe to run again.", lastPhase, code)
	if reason := installerFailureReason(log.location()); reason != "" {
		failureText = reason + " It is safe to run again."
	}
	failureText += " Transcript: " + log.location()
	appendProgress(dataRoot, installProgress{Phase: lastPhase, Text: failureText, Done: true})
	marker.Phase = lastPhase
	_ = writeInstallMarker(dataRoot, marker)
	log.printf("install: the installer exited %d during %s", code, lastPhase)
	if !options.quiet {
		showInstallFailure("Agent_b install failed", failureText)
	}
	return code
}

func installerFailureReason(transcript string) string {
	if strings.TrimSpace(transcript) == "" {
		return ""
	}
	data, err := os.ReadFile(transcript)
	if err != nil {
		return ""
	}
	const marker = "INSTALLATION FAILED:"
	reason := ""
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		at := strings.Index(line, marker)
		if at < 0 {
			continue
		}
		if text := strings.TrimSpace(line[at:]); text != marker {
			reason = text
		}
	}
	return reason
}

func installDataRoot(explicit string, args []string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	return installerArgument(args, "DataDirectory", filepath.Join(os.Getenv("LOCALAPPDATA"), "Agent_b"))
}

func defaultInstallRoot(allUsers bool) string {
	if allUsers {
		return filepath.Join(os.Getenv("ProgramFiles"), "Agent_b")
	}
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Agent_b")
}

func installerFlagPresent(arguments []string, wanted string) bool {
	for _, argument := range arguments {
		if strings.EqualFold(flagName(argument), wanted) {
			return true
		}
	}
	return false
}

func completeInstallMigration(applicationRoot, dataRoot string, testMode bool, log *installLog) (string, error) {
	marker := filepath.Join(dataRoot, "migration-pending.json")
	if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	markerBytes, err := os.ReadFile(marker)
	if err != nil {
		return "", err
	}
	var migration struct {
		LegacyRoot string `json:"legacy_application_directory"`
	}
	if err := json.Unmarshal(markerBytes, &migration); err != nil {
		return "", err
	}
	arguments := []string{"-NoLogo", "-NoProfile", "-File", filepath.Join(applicationRoot, "scripts", "complete-install-migration.ps1"), "-DataDirectory", dataRoot}
	if testMode {
		arguments = append(arguments, "-TestMode")
	}
	command := exec.Command(windowsPowerShell(), arguments...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		text := output.String()
		lower := strings.ToLower(strings.ReplaceAll(text, "\x00", ""))
		if strings.Contains(text, "UnauthorizedAccessException") ||
			strings.Contains(lower, "access to the path") && strings.Contains(lower, "denied") ||
			strings.Contains(lower, "being used by another process") {
			return fmt.Sprintf("MIGRATION LEFT IN PLACE: access denied — remove it from an elevated shell: %s; registered shortcuts and Installed apps point to the per-user copy, so no launcher under this legacy tree is used.", migration.LegacyRoot), nil
		}
		_, _ = io.WriteString(log.writer(), text)
		return "", err
	}
	text := output.String()
	_, _ = io.WriteString(log.writer(), text)
	clean := strings.TrimSpace(strings.ReplaceAll(text, "\x00", ""))
	if strings.Contains(clean, "MIGRATION LEFT IN PLACE:") {
		return clean, nil
	}
	return "", nil
}

func installRestartDetails(path string) (string, string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	version, reason := "", ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if index := strings.Index(line, "RESTART VERSION: "); index >= 0 {
			version = strings.TrimSpace(line[index+len("RESTART VERSION: "):])
		}
		if index := strings.Index(line, "RESTART REASON: "); index >= 0 {
			reason = strings.TrimSpace(line[index+len("RESTART REASON: "):])
		}
	}
	return version, reason, version != "" && reason != ""
}

func installerArgument(arguments []string, name, fallback string) string {
	for index, argument := range arguments {
		trimmed := strings.TrimLeft(argument, "-")
		key, value, hasValue := strings.Cut(trimmed, "=")
		if !strings.EqualFold(key, name) {
			continue
		}
		if hasValue && strings.TrimSpace(value) != "" {
			return value
		}
		if index+1 < len(arguments) && strings.TrimSpace(arguments[index+1]) != "" {
			return arguments[index+1]
		}
	}
	return fallback
}

func launchInstalledAgent(applicationRoot, dataRoot, sessionID string, native bool, log *installLog) error {
	if native {
		executable := filepath.Join(applicationRoot, "Agent_b.exe")
		arguments := []string{"-window", "-config", filepath.Join(dataRoot, "harness.json"), "-app-root", applicationRoot, "-data-root", dataRoot}
		if sessionID != "" {
			arguments = append(arguments, "-reopen-session", sessionID)
		}
		command := quietproc.Quiet(exec.Command(executable, arguments...))
		command.Dir = dataRoot
		command.Stdout, command.Stderr = log.writer(), log.writer()
		if err := command.Start(); err != nil {
			return err
		}
		exited := make(chan error, 1)
		go func() { exited <- command.Wait() }()
		select {
		case err := <-exited:
			if err == nil {
				return fmt.Errorf("installed Agent_b exited before startup completed")
			}
			return err
		case <-time.After(time.Second):
			return nil
		}
	}
	launcher := filepath.Join(applicationRoot, "scripts", "launch-Agent_b.ps1")
	if info, err := os.Stat(launcher); err != nil || info.IsDir() {
		return fmt.Errorf("installed launcher is missing: %s", launcher)
	}
	arguments := []string{"-NoLogo", "-NoProfile", "-File", launcher, "-ApplicationDirectory", applicationRoot, "-DataDirectory", dataRoot, "-Detached", "-NoPause"}
	if sessionID != "" {
		arguments = append(arguments, "-SessionID", sessionID)
	}
	// Item 2m6 (a) and (c): A DISPOSABLE INSTALL NEVER OPENS A WINDOW, and it is
	// decided by WHAT THE INSTALL IS rather than by a flag a future arm can
	// forget.
	//
	// The environment variable below was the only thing keeping the suite's
	// installs headless, and rel-1.24.0/W1 enumerated the suite paths that
	// install: six of them, and only test-installer.ps1 set it. An install to a
	// root that is not the canonical one is by definition not the operator's
	// install -- that is 2kr's disposable-root marker -- so it starts without a
	// window whether or not anyone remembered the variable. The variable still
	// works, as an override for a canonical-root install that wants no window.
	if os.Getenv("AGENT_B_INSTALL_NO_BROWSER") != "" || !canonicalInstallRoot(applicationRoot) {
		arguments = append(arguments, "-NoBrowser")
	}
	command := exec.Command(windowsPowerShell(), arguments...)
	command.Dir = dataRoot
	command.Stdout = log.writer()
	command.Stderr = log.writer()
	return command.Run()
}

func extractInstallBundle(executable string) (string, func(), bool, error) {
	file, err := os.Open(executable)
	if err != nil {
		return "", nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() < installBundleFooterSize {
		return "", nil, false, err
	}
	tailSize := info.Size()
	if tailSize > 4<<20 {
		tailSize = 4 << 20
	}
	tail := make([]byte, tailSize)
	if _, err := file.ReadAt(tail, info.Size()-tailSize); err != nil {
		return "", nil, false, err
	}
	footerIndex := bytes.LastIndex(tail, installBundleMagic)
	if footerIndex < 0 || footerIndex+installBundleFooterSize > len(tail) {
		return "", nil, false, nil
	}
	footer := tail[footerIndex : footerIndex+installBundleFooterSize]
	footerOffset := info.Size() - tailSize + int64(footerIndex)
	length := int64(0)
	for index := 0; index < 8; index++ {
		length |= int64(footer[16+index]) << (8 * index)
	}
	if length <= 0 || length > 256<<20 || length > footerOffset {
		return "", nil, true, errors.New("bundle length is outside the executable")
	}
	offset := footerOffset - length
	section := io.NewSectionReader(file, offset, length)
	hash := sha256.New()
	if _, err := io.Copy(hash, section); err != nil {
		return "", nil, true, err
	}
	if !bytesEqual(hash.Sum(nil), footer[24:]) {
		return "", nil, true, errors.New("bundle SHA-256 does not match its footer")
	}
	archive, err := zip.NewReader(io.NewSectionReader(file, offset, length), length)
	if err != nil {
		return "", nil, true, err
	}
	root, err := os.MkdirTemp("", "Agent_b-setup-")
	if err != nil {
		return "", nil, true, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	fail := func(err error) (string, func(), bool, error) { cleanup(); return "", nil, true, err }
	for _, entry := range archive.File {
		clean := filepath.Clean(filepath.FromSlash(entry.Name))
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return fail(errors.New("bundle contains an unsafe path"))
		}
		if entry.UncompressedSize64 > 64<<20 {
			return fail(errors.New("bundle entry is too large"))
		}
		target := filepath.Join(root, clean)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fail(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fail(err)
		}
		input, err := entry.Open()
		if err != nil {
			return fail(err)
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			input.Close()
			return fail(err)
		}
		_, copyErr := io.Copy(output, io.LimitReader(input, 64<<20))
		closeErr, inputErr := output.Close(), input.Close()
		if copyErr != nil {
			return fail(copyErr)
		}
		if closeErr != nil {
			return fail(closeErr)
		}
		if inputErr != nil {
			return fail(inputErr)
		}
	}
	self := filepath.Join(root, "Agent_b.exe")
	if err := copyFile(executable, self); err != nil {
		return fail(err)
	}
	identity := buildinfo.Current()
	selfHash, err := fileSHA256(executable)
	if err != nil {
		return fail(err)
	}
	manifest := map[string]any{"schema": 1, "tag": identity.Tag, "commit": identity.Commit, "dirty": identity.Dirty, "display": identity.Display, "exe_sha256": selfHash, "exe_bytes": info.Size()}
	encoded, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "candidate-final.json"), append(encoded, '\n'), 0o600); err != nil {
		return fail(err)
	}
	return root, cleanup, true, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func currentDisplayVersion(source string) string {
	content, err := os.ReadFile(filepath.Join(source, "scripts", "install-Agent_b.ps1"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "$displayVersion") {
			continue
		}
		if open := strings.Index(trimmed, "'"); open >= 0 {
			if close := strings.Index(trimmed[open+1:], "'"); close >= 0 {
				return "v" + trimmed[open+1:open+1+close]
			}
		}
	}
	return ""
}

func readInstallProgress(dataRoot string) ([]installProgress, error) {
	file, err := os.Open(installProgressPath(dataRoot))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries := []installProgress{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry installProgress
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return entries, err
	}
	return entries, nil
}

func canonicalInstallRoot(applicationRoot string) bool {
	resolved, err := filepath.Abs(applicationRoot)
	if err != nil {
		return false
	}
	for _, allUsers := range []bool{false, true} {
		canonical, err := filepath.Abs(defaultInstallRoot(allUsers))
		if err != nil || canonical == "" {
			continue
		}
		if strings.EqualFold(resolved, canonical) {
			return true
		}
	}
	return false
}
