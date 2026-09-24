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

// LockPlanFileWrite is LockPlanFile for a file tool writing a plan's plan.md
// (an accepted edit, a planner's own write): the returned unlock announces
// plan.updated when the file changed while it was held, so every route that
// rewrites plan.md reaches the page, not only the worker's markers.
func LockPlanFileWrite(path string) func() {
	unlock := LockPlanFile(path)
	before, beforeErr := os.Stat(path)
	return func() {
		after, afterErr := os.Stat(path)
		unlock()
		if afterErr == nil && (beforeErr != nil || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size()) {
			notifyPlan("plan.updated", filepath.Dir(path))
		}
	}
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

// RegistrationRefusal is the reason a folder may not become a plan's
// repository, judged on the folder the path actually names: junctions and
// links are resolved first, so a link cannot carry a registration into the
// plans folder. The folder must exist, must not be a drive or network root,
// and must neither sit inside the plans folder nor contain it. A network path
// is refused before it is touched, so registration never opens a share. The
// first result is the resolved folder, for the card to show.
func RegistrationRefusal(plansRoot, repo string) (string, string) {
	clean := filepath.Clean(repo)
	if strings.HasPrefix(filepath.VolumeName(clean), `\\`) {
		return "", "a network path cannot be a plan's repository; choose a local folder"
	}
	resolved, err := finalPath(clean)
	if err != nil {
		return "", "this folder does not exist or cannot be read; choose an existing repository"
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return "", "this path is not a folder; choose an existing repository"
	}
	if strings.HasPrefix(filepath.VolumeName(resolved), `\\`) || filepath.Dir(resolved) == resolved {
		return "", "a drive or network root cannot be a plan's repository; choose the repository's own folder"
	}
	// v0.69.0/W16 cold review: registering a folder adds it to every chat's
	// writable union, so the operator's whole connection and the system folders
	// are refused wherever the typed path led.
	if reason := protectedRepoFolder(resolved); reason != "" {
		return "", reason
	}
	if plansRoot != "" {
		root := filepath.Clean(plansRoot)
		if value, err := finalPath(root); err == nil {
			root = value
		}
		if pathWithin(root, resolved) {
			return "", "this repository is inside the plans folder, where nothing may be written; choose a repository outside it"
		}
		if pathWithin(resolved, root) {
			return "", "this folder contains the plans folder; choose the repository's own folder"
		}
	}
	return resolved, ""
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

// protectedRepoFolder is why a resolved folder may not back a plan: the
// operator's connection folder itself, or anything in or equal to the Windows or
// Program Files folders. Folders inside the connection stay usable.
func protectedRepoFolder(resolved string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if real, realErr := finalPath(home); realErr == nil {
			home = real
		}
		if strings.EqualFold(filepath.Clean(home), filepath.Clean(resolved)) {
			return "your whole connection folder cannot be a plan's repository; choose the repository's own folder"
		}
	}
	for _, name := range []string{"SystemRoot", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		root := os.Getenv(name)
		if root == "" {
			continue
		}
		if real, err := finalPath(root); err == nil {
			root = real
		}
		if strings.EqualFold(filepath.Clean(root), filepath.Clean(resolved)) || pathWithin(root, resolved) {
			return "a Windows or Program Files folder cannot be a plan's repository; choose the repository's own folder"
		}
	}
	return ""
}
