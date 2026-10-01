package skills

import (
	"bufio"
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
}

func Inspect(source string) (Skill, string, []string, error) {
	path := filepath.Join(source, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, "", nil, err
	}
	name, description, reason := frontmatter(string(data))
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
	name, description, reason := frontmatter(body)
	return Skill{Name: name, Description: description, Path: source, Valid: reason == "", Reason: reason}, body, files, entries, top, nil
}

var validName = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

func Scan(root string, settings map[string]Setting) ([]Skill, string) {
	entries, _ := os.ReadDir(root)
	list := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		folder := entry.Name()
		path := filepath.Join(root, folder, "SKILL.md")
		data, err := os.ReadFile(path)
		skill := Skill{Path: path, Source: "written here", Valid: false}
		if err != nil {
			skill.Name, skill.Reason = folder, "SKILL.md is missing"
		} else {
			skill.Name, skill.Description, skill.Reason = frontmatter(string(data))
			skill.Valid = skill.Reason == ""
			skill.Warnings = warnings(filepath.Dir(path), string(data))
		}
		key := skill.Name
		if key == "" {
			key = folder
		}
		setting := settings[key]
		skill.Enabled, skill.LastRead = setting.Enabled && skill.Valid, setting.LastRead
		if setting.Source != "" {
			skill.Source = setting.Source
		}
		line := indexLine(skill)
		skill.IndexTokens = estimateTokens(line)
		list = append(list, skill)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	lines := []string{}
	for _, skill := range list {
		if skill.Enabled {
			lines = append(lines, indexLine(skill))
		}
	}
	if len(lines) == 0 {
		return list, ""
	}
	return list, "## Skills\nRead a matching SKILL.md with read_file before using it; run a script only when that skill says to run it.\n" + strings.Join(lines, "\n")
}

func frontmatter(text string) (name, description, reason string) {
	s := bufio.NewScanner(strings.NewReader(text))
	if !s.Scan() || strings.TrimSpace(s.Text()) != "---" || !strings.Contains(text[3:], "\n---") {
		return "", "", "frontmatter must start with ---"
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
			}
		}
	}
	if !validName.MatchString(name) {
		return name, description, "name must be 1-64 lowercase letters, digits, or hyphens"
	}
	if description == "" || len(description) > 1024 {
		return name, description, "description must be 1-1024 characters"
	}
	return name, description, ""
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
func indexLine(s Skill) string {
	return s.Name + " | " + s.Description + " | " + filepath.ToSlash(s.Path)
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
	name, _, reason := frontmatter(string(data))
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
	return Setting{Name: name, Source: "imported from " + absolute}, nil
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
	return Setting{Name: item.Name, Source: "imported from " + absolute}, nil
}
