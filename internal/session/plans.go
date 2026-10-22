package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	workspaceinfo "harness/internal/workspace"
)

const (
	planManifestName = "plan.json"
	repoIndexName    = "repos.json"
	maxRepositories  = 256
)

type Plan struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Repo       string `json:"repo,omitempty"`
	Planning   bool   `json:"planning"`
	Missing    bool   `json:"missing,omitempty"`
	LegacyPath string `json:"legacy_path,omitempty"`
	OpenChats  int    `json:"open_chats,omitempty"`
	LastGo     string `json:"last_go,omitempty"`
}

type planManifest struct {
	Repo string `json:"repo,omitempty"`
}

type repoIndex struct {
	Version  int    `json:"version"`
	Migrated bool   `json:"migrated"`
	Repos    []Plan `json:"repos"`
}

func ListPlans(root string) ([]Plan, error) {
	index, err := readRepoIndex(root)
	if err != nil {
		return nil, err
	}
	values := append([]Plan(nil), index.Repos...)
	for i := range values {
		info, statErr := os.Stat(values[i].Repo)
		values[i].Missing = statErr != nil || !info.IsDir()
		values[i].Planning = !values[i].Missing && regularFile(filepath.Join(values[i].Repo, "plan.md"))
		if !values[i].Missing {
			values[i].Name = filepath.Base(values[i].Repo)
			if values[i].Planning {
				values[i].Name = planDisplayName(values[i].Repo)
			}
		}
	}
	sort.Slice(values, func(i, j int) bool { return strings.ToLower(values[i].Name) < strings.ToLower(values[j].Name) })
	return values, nil
}

func readRepoIndex(root string) (repoIndex, error) {
	path := filepath.Join(root, repoIndexName)
	data, err := os.ReadFile(path)
	if err == nil {
		var index repoIndex
		if err := json.Unmarshal(data, &index); err != nil {
			return repoIndex{}, err
		}
		if index.Repos == nil {
			index.Repos = []Plan{}
		}
		return index, nil
	}
	if !os.IsNotExist(err) {
		return repoIndex{}, err
	}
	entries, readErr := os.ReadDir(root)
	if os.IsNotExist(readErr) {
		return repoIndex{Version: 1, Migrated: true, Repos: []Plan{}}, nil
	}
	if readErr != nil {
		return repoIndex{}, readErr
	}
	index := repoIndex{Version: 1, Migrated: true, Repos: []Plan{}}
	for _, entry := range entries {
		if !entry.IsDir() || len(index.Repos) >= maxRepositories {
			continue
		}
		legacyDir := filepath.Join(root, entry.Name())
		repo := planRepo(legacyDir)
		if repo == "" {
			continue
		}
		item := Plan{ID: entry.Name(), Name: planDisplayName(legacyDir), Repo: filepath.Clean(repo), LegacyPath: legacyDir}
		if info, statErr := os.Stat(item.Repo); statErr != nil || !info.IsDir() {
			item.Missing = true
		} else if err := copyLegacyPlan(filepath.Join(legacyDir, "plan.md"), filepath.Join(item.Repo, "plan.md")); err != nil {
			return repoIndex{}, err
		}
		index.Repos = append(index.Repos, item)
	}
	if err := writeRepoIndex(root, index); err != nil {
		return repoIndex{}, err
	}
	return index, nil
}

func copyLegacyPlan(source, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	in, err := os.Open(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func writeRepoIndex(root string, index repoIndex) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, ".repos-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(root, repoIndexName))
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func planRepo(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, planManifestName))
	if err != nil {
		return ""
	}
	var manifest planManifest
	if json.Unmarshal(data, &manifest) != nil {
		return ""
	}
	return manifest.Repo
}

func writePlanRepo(dir, repo string) error {
	data, err := json.MarshalIndent(planManifest{Repo: repo}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, planManifestName), append(data, '\n'), 0o600)
}

func canonicalRepo(value string) (string, error) {
	abs, key, err := workspaceinfo.Canonical(value)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("repo is required")
	}
	return abs, nil
}

func (r *Registry) EnsurePlan(repo string) (Plan, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensurePlanLocked(repo)
}

// EnablePlanning creates the one opt-in marker, the repository-root plan.md.
// It is called only by the Repos page after the operator presses Add to planning.
func (r *Registry) EnablePlanning(id string) (Plan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	values, err := ListPlans(r.plansRoot)
	if err != nil {
		return Plan{}, err
	}
	for _, item := range values {
		if item.ID != id {
			continue
		}
		if item.Missing {
			return Plan{}, fmt.Errorf("repository is missing")
		}
		if err := os.MkdirAll(filepath.Join(item.Repo, "plan", "items"), 0o700); err != nil {
			return Plan{}, err
		}
		path := filepath.Join(item.Repo, "plan.md")
		if err := UpdatePlanFile(path, func(current string, exists bool) (string, error) {
			if exists {
				return current, nil
			}
			return PlanTemplate(filepath.Base(item.Repo), item.Repo), nil
		}); err != nil {
			return Plan{}, err
		}
		item.Planning = true
		notifyPlan("plan.updated", item.Repo)
		return item, nil
	}
	return Plan{}, fmt.Errorf("repository not found")
}

func (r *Registry) planningRepoLocked(id string) (Plan, bool) {
	values, err := ListPlans(r.plansRoot)
	if err != nil {
		return Plan{}, false
	}
	for _, item := range values {
		if item.ID == id && item.Planning && !item.Missing {
			return item, true
		}
	}
	return Plan{}, false
}

func (r *Registry) ensurePlanLocked(repo string) (Plan, bool, error) {
	canonical, err := canonicalRepo(repo)
	if err != nil {
		return Plan{}, false, err
	}
	if reason := RepoInsidePlans(r.plansRoot, canonical); reason != "" {
		return Plan{}, false, fmt.Errorf("%s", reason)
	}
	if err := os.MkdirAll(r.plansRoot, 0o700); err != nil {
		return Plan{}, false, err
	}
	// judged on the folder the path resolves to, whatever route
	// asked, so no link or ancestor brings the plans folder into a repository.
	// cold review: and that resolved folder is what is stored, so a
	// link re-pointed after registration changes nothing, and two names for one
	// folder find the same plan.
	typed := canonical
	resolved, reason := RegistrationRefusal(r.plansRoot, canonical)
	for _, item := range r.planListLocked() {
		if item.Repo != "" && (samePath(item.Repo, typed) || (resolved != "" && samePath(item.Repo, resolved))) {
			return item, false, nil
		}
	}
	if reason != "" {
		return Plan{}, false, fmt.Errorf("%s", reason)
	}
	canonical = resolved
	if r.planGrant != nil {
		if err := r.planGrant(canonical); err != nil {
			return Plan{}, false, err
		}
	}
	index, err := readRepoIndex(r.plansRoot)
	if err != nil {
		return Plan{}, false, err
	}
	if len(index.Repos) >= maxRepositories {
		return Plan{}, false, fmt.Errorf("repository limit reached")
	}
	planID, err := allocatePlanID(index.Repos)
	if err != nil {
		return Plan{}, false, err
	}
	name := filepath.Base(typed)
	created := Plan{ID: planID, Name: name, Repo: canonical}
	index.Repos = append(index.Repos, created)
	if err := writeRepoIndex(r.plansRoot, index); err != nil {
		return Plan{}, false, err
	}
	notifyPlan("plan.created", canonical)
	return created, true, nil
}

func (r *Registry) planListLocked() []Plan {
	values, err := ListPlans(r.plansRoot)
	if err != nil {
		return []Plan{}
	}
	return values
}

func (r *Registry) planRepos() []string {
	values, err := ListPlans(r.plansRoot)
	if err != nil {
		return nil
	}
	repos := make([]string, 0, len(values))
	for _, value := range values {
		if value.Repo != "" {
			repos = append(repos, filepath.Clean(value.Repo))
		}
	}
	return repos
}

func allocatePlanDir(root string) (string, string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		random := make([]byte, 8)
		if _, err := rand.Read(random); err != nil {
			return "", "", err
		}
		id := hex.EncodeToString(random)
		dir := filepath.Join(root, id)
		if err := os.Mkdir(dir, 0o700); err == nil {
			return id, dir, nil
		} else if !os.IsExist(err) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("could not allocate a unique plan id")
}

func allocatePlanID(plans []Plan) (string, error) {
	used := make(map[string]bool, len(plans))
	for _, item := range plans {
		used[item.ID] = true
	}
	for attempt := 0; attempt < 8; attempt++ {
		random := make([]byte, 8)
		if _, err := rand.Read(random); err != nil {
			return "", err
		}
		id := hex.EncodeToString(random)
		if !used[id] {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique repository id")
}

func samePath(left, right string) bool {
	return SamePath(left, right)
}
