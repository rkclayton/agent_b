package skills

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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

func Import(root, source string) (Setting, error) {
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
	if err := copyTree(source, destination); err != nil {
		return Setting{}, err
	}
	absolute, _ := filepath.Abs(source)
	return Setting{Source: "imported from " + absolute}, nil
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill imports cannot contain links")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}
