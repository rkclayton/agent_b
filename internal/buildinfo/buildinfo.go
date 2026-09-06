package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Commit and Dirty are set by release builds with -ldflags. Ordinary Go builds
// fall back to the VCS metadata embedded by the Go toolchain.
var (
	Commit string
	Dirty  string
)

type Info struct {
	Commit  string `json:"commit"`
	Dirty   bool   `json:"dirty"`
	Known   bool   `json:"known"`
	Source  string `json:"source"`
	Display string `json:"display"`
}

func Current() Info {
	commit := strings.TrimSpace(Commit)
	dirty, dirtyKnown := parseDirty(Dirty)
	source := "ldflags"
	if commit == "" {
		commit, dirty, dirtyKnown = vcsInfo()
		source = "go-vcs"
	}
	if commit == "" {
		return Info{Commit: "unknown", Source: "unknown", Display: "unknown"}
	}
	display := commit
	if len(display) > 12 {
		display = display[:12]
	}
	if dirtyKnown && dirty {
		display += "+dirty"
	}
	return Info{Commit: commit, Dirty: dirtyKnown && dirty, Known: true, Source: source, Display: display}
}

func parseDirty(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "dirty":
		return true, true
	case "false", "0", "clean":
		return false, true
	default:
		return false, false
	}
}

func vcsInfo() (string, bool, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false, false
	}
	var commit string
	var dirty bool
	var dirtyKnown bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			commit = setting.Value
		case "vcs.modified":
			dirty, dirtyKnown = parseDirty(setting.Value)
		}
	}
	return commit, dirty, dirtyKnown
}
