package main

import "strings"

// Item 2gl (v1.2.0/W2): splitting this process's flags from the installer's.
//
// `Agent_b-setup.exe --install -ApplicationDirectory C:\… -TestMode` carries
// two vocabularies: Go's flags for this mode, and the PowerShell installer's
// parameters, which are `-Name value` and would make Go's flag package choke.
// Rather than restate the installer's parameters here — where they would drift
// from the script that actually reads them — anything that is not one of this
// mode's own flags is passed through untouched.

// installModeFlags are the only flags this process interprets.
var installModeFlags = map[string]bool{
	"install":        true,
	"quiet":          true,
	"install-source": true,
	"install-data":   true,
	"version":        true,
	"config":         true,
	"app-root":       true,
	"data-root":      true,
	"replay":         true,
	"startup-log":    true,
}

// flagName reads the name out of -name, --name or --name=value; it returns ""
// for anything that is not a flag at all.
func flagName(argument string) string {
	if !strings.HasPrefix(argument, "-") || argument == "-" || argument == "--" {
		return ""
	}
	name := strings.TrimLeft(argument, "-")
	if index := strings.Index(name, "="); index >= 0 {
		name = name[:index]
	}
	return name
}

// takesValue is true for this mode's flags that are followed by a value.
func takesValue(name string) bool {
	switch name {
	case "install-source", "install-data", "config", "app-root", "data-root", "replay", "startup-log":
		return true
	}
	return false
}

// installFlagArgs is the subset Go's flag package should see.
func installFlagArgs(arguments []string) []string {
	kept := []string{}
	for index := 0; index < len(arguments); index++ {
		name := flagName(arguments[index])
		if name == "" || !installModeFlags[name] {
			continue
		}
		kept = append(kept, arguments[index])
		if takesValue(name) && !strings.Contains(arguments[index], "=") && index+1 < len(arguments) {
			index++
			kept = append(kept, arguments[index])
		}
	}
	return kept
}

// installPassthrough is everything else, in order: the installer's own
// parameters and their values.
func installPassthrough(arguments []string) []string {
	passed := []string{}
	for index := 0; index < len(arguments); index++ {
		name := flagName(arguments[index])
		if name != "" && installModeFlags[name] {
			if takesValue(name) && !strings.Contains(arguments[index], "=") && index+1 < len(arguments) {
				index++
			}
			continue
		}
		passed = append(passed, arguments[index])
	}
	return passed
}
