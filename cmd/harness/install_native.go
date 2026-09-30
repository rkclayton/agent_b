package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type nativeInstallPlan struct {
	Source, Application, Data, Workspace, StartMenu, SendTo string
	Version, OperatorSID                                    string
}

type shortcutSpec struct {
	Path, Target, Arguments, WorkingDirectory, Icon, Description string
}

type nativeInstallPlatform struct {
	shortcut func(shortcutSpec) error
	register func(map[string]any) error
	secure   func(string, string, bool) error
}

func verifyNativeCandidate(source string) error {
	encoded, err := os.ReadFile(filepath.Join(source, "candidate-final.json"))
	if err != nil {
		return fmt.Errorf("candidate identity: %w", err)
	}
	var identity struct {
		EXESHA256 string `json:"exe_sha256"`
		EXEBytes  int64  `json:"exe_bytes"`
		Display   string `json:"display"`
	}
	if err := json.Unmarshal(encoded, &identity); err != nil {
		return fmt.Errorf("candidate identity: %w", err)
	}
	executable := filepath.Join(source, "Agent_b.exe")
	digest, err := fileSHA256(executable)
	if err != nil {
		return err
	}
	info, err := os.Stat(executable)
	if err != nil {
		return err
	}
	if !strings.EqualFold(digest, identity.EXESHA256) || identity.EXEBytes != info.Size() || strings.TrimSpace(identity.Display) == "" {
		return fmt.Errorf("candidate identity does not match Agent_b.exe")
	}
	return nil
}

func installPerUserNative(plan nativeInstallPlan, platform nativeInstallPlatform) error {
	for label, root := range map[string]string{"source": plan.Source, "application": plan.Application, "data": plan.Data} {
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("%s directory is required", label)
		}
	}
	application, err := filepath.Abs(plan.Application)
	if err != nil {
		return err
	}
	data, err := filepath.Abs(plan.Data)
	if err != nil {
		return err
	}
	if pathWithin(application, data) || pathWithin(data, application) {
		return fmt.Errorf("application and operator-data directories must be disjoint")
	}
	if err := os.MkdirAll(application, 0o700); err != nil {
		return err
	}
	if platform.secure != nil {
		if err := platform.secure(application, plan.OperatorSID, false); err != nil {
			return fmt.Errorf("application ACL: %w", err)
		}
	}
	for _, directory := range []string{"web", "prompts", "scripts", "docs"} {
		if err := replaceDirectory(filepath.Join(plan.Source, directory), filepath.Join(application, directory)); err != nil {
			return fmt.Errorf("copy %s: %w", directory, err)
		}
	}
	for _, name := range []string{"Agent_b.exe", "agentb.exe", "WebView2Loader.dll", "harness.example.json", "SECURITY.md", "LICENSE", "NOTICE"} {
		if err := copyNativeFile(filepath.Join(plan.Source, name), filepath.Join(application, name)); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
	}
	if err := copyNativeFile(filepath.Join(plan.Source, "scripts", "launch-installed.cmd"), filepath.Join(application, "Agent_b.cmd")); err != nil {
		return fmt.Errorf("copy Agent_b.cmd: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(data, "logs"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(data, "memory"), 0o700); err != nil {
		return err
	}
	if platform.secure != nil {
		if err := platform.secure(data, plan.OperatorSID, true); err != nil {
			return fmt.Errorf("operator-data ACL: %w", err)
		}
	}
	configPath := filepath.Join(data, "harness.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := createNativeConfig(filepath.Join(application, "harness.example.json"), configPath, plan.Workspace, data); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	executable := filepath.Join(application, "Agent_b.exe")
	if platform.shortcut != nil {
		start := shortcutSpec{Path: filepath.Join(plan.StartMenu, "Agent_b.lnk"), Target: executable,
			Arguments:        `-window -config "` + configPath + `" -app-root "` + application + `" -data-root "` + data + `"`,
			WorkingDirectory: data, Icon: filepath.Join(application, "web", "assets", "Agent_b.ico") + ",0", Description: "Open Agent_b"}
		if err := platform.shortcut(start); err != nil {
			return fmt.Errorf("Start-menu shortcut: %w", err)
		}
		send := shortcutSpec{Path: filepath.Join(plan.SendTo, "Agent_b.lnk"), Target: executable,
			Arguments: `--send-to "` + filepath.Join(data, "exchange", "attachments") + `"`, WorkingDirectory: data, Icon: start.Icon, Description: "Copy the selected files into Agent_b"}
		if err := platform.shortcut(send); err != nil {
			return fmt.Errorf("SendTo shortcut: %w", err)
		}
	}
	size, err := treeBytes(application)
	if err != nil {
		return err
	}
	if platform.register != nil {
		uninstall := quoteWindowsArgument(executable) + " --uninstall --app-root " + quoteWindowsArgument(application) + " --data-root " + quoteWindowsArgument(data)
		values := map[string]any{"DisplayName": "Agent_b", "DisplayVersion": plan.Version, "Publisher": "acme", "DisplayIcon": filepath.Join(application, "web", "assets", "Agent_b.ico"), "InstallLocation": application, "UninstallString": uninstall, "QuietUninstallString": uninstall + " --quiet", "URLInfoAbout": "https://github.com/rkclayton/agent_b", "OperatorSid": plan.OperatorSID, "DataLocation": data, "WorkspaceLocation": plan.Workspace, "EstimatedSize": uint32((size + 1023) / 1024), "NoModify": uint32(1), "NoRepair": uint32(1)}
		if err := platform.register(values); err != nil {
			return fmt.Errorf("Installed apps registration: %w", err)
		}
	}
	return nil
}
func pathWithin(child, parent string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
func replaceDirectory(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("required directory is missing: %s", source)
	}
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing link in application payload: %s", path)
		}
		relative, _ := filepath.Rel(source, path)
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyNativeFile(path, target)
	})
}
func copyNativeFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	temporary := destination + ".installing"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(temporary)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(temporary)
		return closeErr
	}
	_ = os.Remove(destination)
	return os.Rename(temporary, destination)
}
func createNativeConfig(template, destination, workspace, data string) error {
	encoded, err := os.ReadFile(template)
	if err != nil {
		return err
	}
	var config map[string]any
	if err := json.Unmarshal(encoded, &config); err != nil {
		return fmt.Errorf("configuration template: %w", err)
	}
	config["workspace"], config["log_dir"] = workspace, filepath.Join(data, "logs")
	memory, _ := config["memory"].(map[string]any)
	if memory == nil {
		memory = map[string]any{}
		config["memory"] = memory
	}
	memory["dir"] = filepath.Join(data, "memory")
	encoded, _ = json.MarshalIndent(config, "", "  ")
	return os.WriteFile(destination, append(encoded, '\n'), 0o600)
}
func treeBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			info, statErr := entry.Info()
			if statErr != nil {
				return statErr
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
func quoteWindowsArgument(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}
