package session

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// planFiles holds one lock per plan.md in this process. Every writer of a plan
// file — its creation, the worker's markers, the tray's accepted edits and the
// planner's own file tools — takes the same lock, so a marker change and an
// accepted proposal landing in the same second cannot lose each other.
var planFiles sync.Map

func planFileKey(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return strings.ToLower(filepath.Clean(path))
}

// LockPlanFile takes the one writer lock for a plan file and returns its release.
func LockPlanFile(path string) func() {
	value, _ := planFiles.LoadOrStore(planFileKey(path), &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// UpdatePlanFile is the serialised writer: under the plan file's lock it re-reads
// the file as it is now, hands that text to change, and writes the result. A
// change computed from an earlier read can therefore never overwrite a write
// that landed after it.
func UpdatePlanFile(path string, change func(current string, exists bool) (string, error)) error {
	unlock := LockPlanFile(path)
	defer unlock()
	data, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next, err := change(string(data), exists)
	if err != nil {
		return err
	}
	if exists && next == string(data) {
		return nil
	}
	if err := os.WriteFile(path, []byte(next), 0o600); err != nil {
		return err
	}
	// A new plan's creator announces it once its folder is complete; a rewrite
	// of an existing plan.md is announced here, whoever wrote it (item 2bq).
	if exists && strings.EqualFold(filepath.Base(path), "plan.md") {
		notifyPlan("plan.updated", filepath.Dir(path))
	}
	return nil
}

// PlanFileFor reports the plan.md a d session's file-tool path names, if it
// names one. Relative paths resolve against the session's plan folder, which is
// the only root a d session writes.
func (s *Session) PlanFileFor(path string) (string, bool) {
	s.mu.Lock()
	planDir, plansRoot := s.PlanDir, s.PlansRoot
	s.mu.Unlock()
	if plansRoot == "" || path == "" {
		return "", false
	}
	candidate := filepath.FromSlash(path)
	if !filepath.IsAbs(candidate) {
		if planDir == "" {
			return "", false
		}
		candidate = filepath.Join(planDir, candidate)
	}
	candidate = filepath.Clean(candidate)
	if !strings.EqualFold(filepath.Base(candidate), "plan.md") || !samePath(filepath.Dir(filepath.Dir(candidate)), plansRoot) {
		return "", false
	}
	return candidate, true
}

// RepoInsidePlans is the reason a repository cannot back a plan: a repository
// inside the plans folder is one no chat and no worker may write, so a worker
// on it could only ever be refused. Empty when the repository is usable.
func RepoInsidePlans(plansRoot, repo string) string {
	if plansRoot == "" || repo == "" {
		return ""
	}
	if pathWithin(filepath.Clean(plansRoot), filepath.Clean(repo)) {
		return "this repository is inside the plans folder, where nothing may be written; choose a repository outside it"
	}
	return ""
}
