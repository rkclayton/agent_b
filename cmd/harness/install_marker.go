package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Item 2gl (v1.2.0/W2): an install that was interrupted says so afterwards.
//
// The operator: "i ran this installer and might've broken agentb by closing the
// window that had the preflight language in it. should i re-run? we should have
// some logic to catch this." Today nothing records that an install began, so a
// half-finished install is indistinguishable from a finished one and the only
// way to find out is to run it again and hope.
//
// The marker is a small JSON file in the operator's data root, written before
// the install does anything and removed when it finishes. Finding one at
// startup means the last install did not reach its end. It is deliberately
// NOT in the application root: the application root is what an install
// replaces, and a marker there would be destroyed by the very failure it
// exists to record.

// InstallMarker is what an install in flight leaves behind.
type InstallMarker struct {
	// Phase is the last phase the install announced, so the operator is told
	// how far it got rather than only that it stopped.
	Phase string `json:"phase"`
	// StartedAt and UpdatedAt are RFC3339; UpdatedAt moves with each phase.
	StartedAt string `json:"started_at"`
	UpdatedAt string `json:"updated_at"`
	// Version and Source identify what was being installed, so a marker from
	// an older attempt is not mistaken for this one.
	Version string `json:"version"`
	Source  string `json:"source"`
	// PID is the installing process, for a human reading the file.
	PID int `json:"pid"`
	// Quiet records that the install was the suite's, not the operator's.
	Quiet bool `json:"quiet,omitempty"`
}

const installMarkerName = "install-in-progress.json"

// installMarkerPath is the marker for a data root.
func installMarkerPath(dataRoot string) string {
	return filepath.Join(dataRoot, installMarkerName)
}

// writeInstallMarker records that an install has begun, or moves it to its
// next phase. It is written whole each time: a partial marker is worse than
// none, so it is written to a temporary file and renamed over the old one.
func writeInstallMarker(dataRoot string, marker InstallMarker) error {
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return err
	}
	marker.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if marker.StartedAt == "" {
		marker.StartedAt = marker.UpdatedAt
	}
	if marker.PID == 0 {
		marker.PID = os.Getpid()
	}
	encoded, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	path := installMarkerPath(dataRoot)
	temporary := path + ".writing"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// readInstallMarker returns the marker, or ok=false when there is none. A
// marker that cannot be parsed is reported as an error rather than ignored:
// something wrote it, and pretending it is absent is how an interrupted
// install becomes invisible again.
func readInstallMarker(dataRoot string) (InstallMarker, bool, error) {
	content, err := os.ReadFile(installMarkerPath(dataRoot))
	if errors.Is(err, os.ErrNotExist) {
		return InstallMarker{}, false, nil
	}
	if err != nil {
		return InstallMarker{}, false, err
	}
	var marker InstallMarker
	if err := json.Unmarshal(content, &marker); err != nil {
		return InstallMarker{}, true, fmt.Errorf("the install marker at %s is unreadable: %w", installMarkerPath(dataRoot), err)
	}
	return marker, true, nil
}

// clearInstallMarker removes the marker. An install that finishes clears it;
// nothing else does, so the file's absence means "the last install finished".
func clearInstallMarker(dataRoot string) error {
	err := os.Remove(installMarkerPath(dataRoot))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// describeInterruptedInstall is the line the operator reads at the next launch.
// It answers his actual question — "should i re-run?" — rather than reporting
// a file.
func describeInterruptedInstall(marker InstallMarker) string {
	phase := marker.Phase
	if phase == "" {
		phase = "an early phase"
	}
	started := marker.StartedAt
	if started == "" {
		started = "an unrecorded time"
	}
	version := marker.Version
	if version == "" {
		version = "an unrecorded version"
	}
	return fmt.Sprintf("the last install of %s did not finish: it stopped during %s, begun %s. Run the installer again — it is safe to repeat and will complete the install.", version, phase, started)
}
