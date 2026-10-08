package skills

import (
	"bufio"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"harness/internal/attachment"
	"harness/internal/config"
)

type Setting = config.SkillSetting
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Path        string   `json:"path"`
	Source      string   `json:"source"`
	LastRead    string   `json:"last_read"`
	Reason      string   `json:"reason,omitempty"`
	Enabled     bool     `json:"enabled"`
	Valid       bool     `json:"valid"`
	IndexTokens int      `json:"index_tokens"`
	Warnings    []string `json:"warnings,omitempty"`
	Included    bool     `json:"included,omitempty"`
	State       bool     `json:"state,omitempty"`
	StatePath   string   `json:"state_path,omitempty"`
}

//go:embed included/*/SKILL.md
var included embed.FS

func EnsureIncluded(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("included skills root must be absolute")
	}
	entries, _ := included.ReadDir("included")
	for _, entry := range entries {
		data, err := included.ReadFile("included/" + entry.Name() + "/SKILL.md")
		if err != nil {
			return err
		}
		path := filepath.Join(root, ".included", entry.Name(), "SKILL.md")
		if current, readErr := os.ReadFile(path); readErr == nil && string(current) == string(data) {
			continue
		}
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err = os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Load returns the user copy when one exists and otherwise the shipped copy.
// The state path is present only for a skill that declares state: true.
func Load(root, stateRoot, name string) (body, skillPath, statePath string, err error) {
	if !validName.MatchString(name) {
		return "", "", "", fmt.Errorf("invalid skill name %q", name)
	}
	userDir := filepath.Join(root, name)
	skillPath = filepath.Join(userDir, "SKILL.md")
	_, statErr := os.Stat(userDir)
	var data []byte
	var readErr error
	if statErr == nil {
		data, readErr = os.ReadFile(skillPath)
	} else if os.IsNotExist(statErr) {
		skillPath = filepath.Join(root, ".included", name, "SKILL.md")
		data, readErr = os.ReadFile(skillPath)
	} else {
		readErr = statErr
	}
	if readErr != nil {
		return "", "", "", readErr
	}
	parsedName, _, state, reason := frontmatter(string(data))
	if reason != "" || parsedName != name {
		if reason == "" {
			reason = "frontmatter name does not match folder"
		}
		return "", "", "", fmt.Errorf("%s", reason)
	}
	if state {
		statePath = filepath.Join(stateRoot, name)
	}
	return string(data), skillPath, statePath, nil
}

func Inspect(source string) (Skill, string, []string, error) {
	path := filepath.Join(source, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, "", nil, err
	}
	name, description, _, reason := frontmatter(string(data))
	item := Skill{Name: name, Description: description, Path: path, Valid: reason == "", Reason: reason}
	files := []string{}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			rel, _ := filepath.Rel(source, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return item, string(data), files, err
}

// InspectSource previews either import shape without copying it. Chat proposals
// use this before raising the same card as a proposal drafted in the workspace.
func InspectSource(source string, max int64) (Skill, string, []string, error) {
	if !strings.EqualFold(filepath.Ext(source), ".zip") {
		return Inspect(source)
	}
	item, body, files, _, _, err := inspectZIP(source, max)
	return item, body, files, err
}

func inspectZIP(source string, max int64) (Skill, string, []string, map[string]attachment.ZIPFile, string, error) {
	entries, _, err := attachment.ReadZIP(source, max)
	if err != nil {
		return Skill{}, "", nil, nil, "", err
	}
	files, body, top := make([]string, 0, len(entries)), "", ""
	for entryName, entry := range entries {
		clean := path.Clean(strings.ReplaceAll(entryName, `\`, "/"))
		folder, relative, present := strings.Cut(strings.Trim(clean, "/"), "/")
		if entry.Refused != "" || !present || (top != "" && folder != top) {
			return Skill{}, "", nil, nil, "", fmt.Errorf("zip must contain one safe skill folder")
		}
		top = folder
		files = append(files, relative)
		if relative == "SKILL.md" {
			body = string(entry.Data)
		}
	}
	if body == "" {
		return Skill{}, "", nil, nil, "", fmt.Errorf("SKILL.md is missing")
	}
	sort.Strings(files)
	name, description, _, reason := frontmatter(body)
	return Skill{Name: name, Description: description, Path: source, Valid: reason == "", Reason: reason}, body, files, entries, top, nil
}

var validName = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

func Scan(root, stateRoot string, settings map[string]Setting) ([]Skill, string) {
	entries, _ := os.ReadDir(root)
	list := make([]Skill, 0, len(entries))
	user := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			user[entry.Name()] = true
		}
	}
	includedEntries, _ := os.ReadDir(filepath.Join(root, ".included"))
	entries = append(entries, includedEntries...)
	seen := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		folder := entry.Name()
		if seen[folder] {
			continue
		}
		seen[folder] = true
		isIncluded := !user[folder]
		path := filepath.Join(root, folder, "SKILL.md")
		if isIncluded {
			path = filepath.Join(root, ".included", folder, "SKILL.md")
		}
		data, err := os.ReadFile(path)
		skill := Skill{Path: path, Source: "written here", Valid: false, Included: isIncluded}
		if isIncluded {
			skill.Source = "included"
		}
		if err != nil {
			skill.Name, skill.Reason = folder, "SKILL.md is missing"
		} else {
			skill.Name, skill.Description, skill.State, skill.Reason = frontmatter(string(data))
			skill.Valid = skill.Reason == ""
			skill.Warnings = warnings(filepath.Dir(path), string(data))
			if skill.State {
				skill.StatePath = filepath.Join(stateRoot, skill.Name)
			}
		}
		key := skill.Name
		if key == "" {
			key = folder
		}
		setting, configured := settings[key]
		skill.Enabled, skill.LastRead = (!configured || setting.Enabled) && skill.Valid, setting.LastRead
		if setting.Source != "" && !isIncluded {
			skill.Source = setting.Source
		}
		skill.IndexTokens = estimateTokens(promptIndexLine(skill))
		list = append(list, skill)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	lines := []string{}
	hasState := false
	for _, skill := range list {
		if skill.Enabled {
			lines = append(lines, promptIndexLine(skill))
			hasState = hasState || skill.StatePath != ""
		}
	}
	if len(lines) == 0 {
		return list, ""
	}
	header := "## Skills\nRead ROOT/<name>/SKILL.md; i names use ROOT/.included/<name>/SKILL.md.\nROOT=" + filepath.ToSlash(root)
	if hasState {
		header += "\ns names store files in STATE/<name>. STATE=" + filepath.ToSlash(stateRoot)
	}
	return list, header + "\n" + strings.Join(lines, "\n")
}

func frontmatter(text string) (name, description string, state bool, reason string) {
	s := bufio.NewScanner(strings.NewReader(text))
	if !s.Scan() || strings.TrimSpace(s.Text()) != "---" || !strings.Contains(text[3:], "\n---") {
		return "", "", false, "frontmatter must start with ---"
	}
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "---" {
			break
		}
		if key, value, ok := strings.Cut(line, ":"); ok {
			switch strings.TrimSpace(key) {
			case "name":
				name = strings.Trim(strings.TrimSpace(value), `"'`)
			case "description":
				description = strings.Trim(strings.TrimSpace(value), `"'`)
			case "state":
				state = strings.EqualFold(strings.TrimSpace(value), "true")
			}
		}
	}
	if !validName.MatchString(name) {
		return name, description, state, "name must be 1-64 lowercase letters, digits, or hyphens"
	}
	if description == "" || len(description) > 1024 {
		return name, description, state, "description must be 1-1024 characters"
	}
	return name, description, state, ""
}

var link = regexp.MustCompile(`\]\(([^)]+)\)`)

func warnings(dir, text string) []string {
	out := []string{}
	if strings.Count(text, "\n")+1 > 500 {
		out = append(out, "SKILL.md is over 500 lines")
	}
	for _, match := range link.FindAllStringSubmatch(text, -1) {
		ref := strings.TrimSpace(strings.Split(match[1], "#")[0])
		if ref == "" || strings.Contains(ref, "://") {
			continue
		}
		if strings.Contains(ref, `\`) {
			out = append(out, "references must use forward slashes")
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(ref))
		if strings.Count(clean, "/") > 1 || strings.HasPrefix(clean, "../") {
			out = append(out, "references may be only one level deep")
			continue
		}
		if data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(clean))); err == nil && strings.Count(string(data), "\n")+1 > 100 && !regexp.MustCompile(`(?mi)^#{1,6}\s+contents\s*$`).Match(data) {
			out = append(out, ref+" is over 100 lines without a contents heading")
		}
	}
	return out
}
func promptIndexLine(s Skill) string {
	prefix := "u "
	if s.Included {
		prefix = "i "
	}
	line := prefix + s.Name + " | " + conciseDescription(s.Description, 102)
	if s.StatePath != "" {
		line += " | s"
	}
	return line
}
func conciseDescription(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	tail := limit / 2
	return strings.TrimSpace(string(runes[:limit-tail-3])) + " … " + strings.TrimSpace(string(runes[len(runes)-tail:]))
}
func estimateTokens(text string) int { n := len([]rune(text)); return (n + 3) / 4 }

func Import(root, source string, max int64) (Setting, error) {
	if strings.EqualFold(filepath.Ext(source), ".zip") {
		return importZIP(root, source, max)
	}
	data, err := os.ReadFile(filepath.Join(source, "SKILL.md"))
	if err != nil {
		return Setting{}, err
	}
	name, _, _, reason := frontmatter(string(data))
	if reason != "" {
		return Setting{}, fmt.Errorf("%s", reason)
	}
	destination := filepath.Join(root, name)
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		return Setting{}, fmt.Errorf("skill %q already exists", name)
	}
	if err := os.CopyFS(destination, os.DirFS(source)); err != nil {
		return Setting{}, err
	}
	absolute, _ := filepath.Abs(source)
	return Setting{Name: name, Source: "imported from " + absolute, Enabled: false}, nil
}

func importZIP(root, source string, max int64) (Setting, error) {
	item, _, _, files, _, err := inspectZIP(source, max)
	if err != nil {
		return Setting{}, err
	}
	if !item.Valid {
		return Setting{}, fmt.Errorf("%s", item.Reason)
	}
	destination := filepath.Join(root, item.Name)
	if _, err = os.Stat(destination); !os.IsNotExist(err) {
		return Setting{}, fmt.Errorf("skill %q already exists", item.Name)
	}
	for entryName, entry := range files {
		_, relative, present := strings.Cut(strings.Trim(path.Clean(strings.ReplaceAll(entryName, `\`, "/")), "/"), "/")
		if !present || strings.HasSuffix(entryName, "/") {
			continue
		}
		target := filepath.Join(destination, filepath.FromSlash(relative))
		if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return Setting{}, err
		}
		if err = os.WriteFile(target, entry.Data, 0o600); err != nil {
			return Setting{}, err
		}
	}
	absolute, _ := filepath.Abs(source)
	return Setting{Name: item.Name, Source: "imported from " + absolute, Enabled: false}, nil
}
