package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const repositoryLimit = 256

type Repository struct {
	Path    string `json:"repo"`
	Name    string `json:"name"`
	Missing bool   `json:"missing,omitempty"`
}

type repositoryIndex struct {
	Repositories []Repository `json:"repos"`
}

func readRepositories(profile string) ([]Repository, error) {
	if profile == "" {
		return []Repository{}, nil
	}
	for _, path := range []string{filepath.Join(profile, "repos.json"), filepath.Join(profile, "plans", "repos.json")} {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var index repositoryIndex
		if err = json.Unmarshal(data, &index); err != nil {
			return nil, err
		}
		values := make([]Repository, 0, min(len(index.Repositories), repositoryLimit))
		for _, entry := range index.Repositories {
			if !filepath.IsAbs(entry.Path) || len(values) == repositoryLimit {
				continue
			}
			entry.Path = filepath.Clean(entry.Path)
			info, statErr := os.Stat(entry.Path)
			entry.Missing = statErr != nil || !info.IsDir()
			entry.Name = filepath.Base(entry.Path)
			values = append(values, entry)
		}
		sort.Slice(values, func(i, j int) bool {
			return strings.ToLower(values[i].Path) < strings.ToLower(values[j].Path)
		})
		return values, nil
	}
	return []Repository{}, nil
}

func (r *Registry) Repositories() ([]Repository, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return readRepositories(r.profileRoot)
}

func (r *Registry) AddRepository(folder string) (Repository, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !filepath.IsAbs(folder) {
		return Repository{}, fmt.Errorf("choose an existing folder")
	}
	path, refusal := RepositoryRegistration(r.dataRoot, folder)
	if refusal != "" {
		return Repository{}, fmt.Errorf("%s", refusal)
	}
	if r.profileRoot == "" {
		return Repository{}, fmt.Errorf("repository storage is unavailable")
	}
	values, err := readRepositories(r.profileRoot)
	if err != nil {
		return Repository{}, err
	}
	for _, value := range values {
		if SamePath(value.Path, path) {
			return value, nil
		}
	}
	if len(values) >= repositoryLimit {
		return Repository{}, fmt.Errorf("repository limit reached")
	}
	if r.repositoryGrant != nil {
		if err := r.repositoryGrant(path); err != nil {
			return Repository{}, err
		}
	}
	value := Repository{Path: path, Name: filepath.Base(path)}
	values = append(values, value)
	data, err := json.MarshalIndent(repositoryIndex{Repositories: values}, "", "  ")
	if err != nil {
		return Repository{}, err
	}
	file, err := os.CreateTemp(r.profileRoot, ".repos-*.tmp")
	if err != nil {
		return Repository{}, err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(append(data, '\n'))
	closeErr := file.Close()
	if writeErr != nil {
		return Repository{}, writeErr
	}
	if closeErr != nil {
		return Repository{}, closeErr
	}
	return value, os.Rename(file.Name(), filepath.Join(r.profileRoot, "repos.json"))
}

func (r *Registry) CreateInRepository(label, agentID, path string) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	repos, err := readRepositories(r.profileRoot)
	if err != nil {
		return nil, err
	}
	for _, repo := range repos {
		if !repo.Missing && SamePath(repo.Path, path) {
			return r.createLocked(label, agentID, repo.Path, nil, "b", "operator")
		}
	}
	return nil, fmt.Errorf("select a registered repository")
}

func RepositoryRegistration(dataRoot, repo string) (string, string) {
	clean := filepath.Clean(repo)
	if strings.HasPrefix(filepath.VolumeName(clean), `\\`) {
		return "", "a network path cannot be a registered repository; choose a local folder"
	}
	resolved, err := finalPath(clean)
	if err != nil {
		return "", "this folder does not exist or cannot be read; choose an existing repository"
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return "", "this path is not a folder; choose an existing repository"
	}
	if strings.HasPrefix(filepath.VolumeName(resolved), `\\`) || filepath.Dir(resolved) == resolved {
		return "", "a drive or network root cannot be a registered repository; choose the repository's own folder"
	}
	// Registration may grant the tool identity access to this folder.
	if reason := protectedRepoFolder(resolved); reason != "" {
		return "", reason
	}
	if dataRoot != "" {
		root := filepath.Clean(dataRoot)
		if value, err := finalPath(root); err == nil {
			root = value
		}
		if pathWithin(root, resolved) {
			return "", "this repository is inside the application data folder; choose a repository outside it"
		}
		if pathWithin(resolved, root) {
			return "", "this folder contains the application data folder; choose the repository's own folder"
		}
	}
	return resolved, ""
}

func protectedRepoFolder(resolved string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if real, realErr := finalPath(home); realErr == nil {
			home = real
		}
		if strings.EqualFold(filepath.Clean(home), filepath.Clean(resolved)) {
			return "your home folder cannot be a registered repository; choose the repository's own folder"
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
			return "a Windows or Program Files folder cannot be a registered repository; choose the repository's own folder"
		}
	}
	return ""
}
