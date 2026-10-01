package hermes

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type Row struct {
	ID                  string   `json:"id"`
	Kind                string   `json:"kind"`
	Name                string   `json:"name"`
	Description         string   `json:"description,omitempty"`
	Detail              string   `json:"detail"`
	Bytes               int64    `json:"bytes,omitempty"`
	Included            bool     `json:"included"`
	Selectable          bool     `json:"selectable"`
	RequiredEnvironment []string `json:"required_environment,omitempty"`
	Calls               []string `json:"calls,omitempty"`
	source              string
}
type Preview struct {
	Rows        []Row `json:"rows"`
	SecretCount int   `json:"secret_count"`
}
type ImportedSkill struct {
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Calls   []string `json:"calls,omitempty"`
	Changed bool     `json:"-"`
}
type Report struct {
	Changed    bool            `json:"changed"`
	Message    string          `json:"message"`
	Skills     []ImportedSkill `json:"skills,omitempty"`
	Rewritten  []string        `json:"rewritten,omitempty"`
	Unresolved []string        `json:"unresolved,omitempty"`
	Skipped    []string        `json:"skipped,omitempty"`
	Cut        []string        `json:"cut,omitempty"`
}
type record struct{ Source, Destination string }
type manifest struct {
	Files map[string]record `json:"files"`
}

var invalidName = regexp.MustCompile(`[^a-z0-9]+`)
var unsupported = []string{"terminal", "process", "execute_code", "web_extract", "delegate_task", "todo", "memory", "cronjob", "clarify"}

const maxImportFiles, maxImportBytes = 2048, 64 << 20

func PreviewHome(root string) (Preview, error) {
	root = filepath.Clean(root)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return Preview{}, fmt.Errorf("Hermes folder %s was not found", root)
	}
	rows := []Row{}
	fileCount, totalBytes := 0, int64(0)
	addFile := func(id, kind, name, relative, detail string, include, selectable bool) {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			if selectable {
				fileCount++
				totalBytes += info.Size()
			}
			rows = append(rows, Row{ID: id, Kind: kind, Name: name, Detail: detail, Bytes: info.Size(), Included: include, Selectable: selectable, source: path})
		}
	}
	addFile("persona:SOUL.md", "persona", "SOUL.md", "SOUL.md", "not imported — it becomes an agent of your own when agents ship", false, false)
	addFile("memory:MEMORY.md", "memory", "MEMORY.md", "memories/MEMORY.md", "memory", true, true)
	addFile("memory:USER.md", "memory", "USER.md", "memories/USER.md", "memory", true, true)
	skillsRoot := filepath.Join(root, "skills")
	walkErr := filepath.WalkDir(skillsRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry == nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			if info, infoErr := entry.Info(); infoErr == nil {
				fileCount++
				totalBytes += info.Size()
				if fileCount > maxImportFiles || totalBytes > maxImportBytes {
					return fmt.Errorf("Hermes import exceeds %d files or %d bytes", maxImportFiles, maxImportBytes)
				}
			}
		}
		if entry.IsDir() || !strings.EqualFold(entry.Name(), "SKILL.md") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		name, description, environment := metadata(string(body))
		skillName := slug(name)
		if skillName == "" {
			skillName = slug(filepath.Base(filepath.Dir(path)))
		}
		calls := skillCalls(filepath.Dir(path))
		detail := description
		if len(environment) > 0 {
			detail += " · needs " + strings.Join(environment, ", ")
		}
		rows = append(rows, Row{ID: "skill:" + skillName, Kind: "skill", Name: skillName, Description: description, Detail: detail, Included: true, Selectable: true, RequiredEnvironment: environment, Calls: calls, source: filepath.Dir(path)})
		return nil
	})
	if walkErr != nil {
		return Preview{}, walkErr
	}
	if fileCount > maxImportFiles || totalBytes > maxImportBytes {
		return Preview{}, fmt.Errorf("Hermes import exceeds %d files or %d bytes", maxImportFiles, maxImportBytes)
	}
	secretNames := envNames(filepath.Join(root, ".env"))
	for _, name := range secretNames {
		rows = append(rows, Row{ID: "secret:" + name, Kind: "secret", Name: name, Detail: "imported in the next step"})
	}
	for _, item := range []struct{ name, kind, detail string }{{"cron", "cron", "not imported — Agent_b has no scheduler"}, {"sessions", "sessions", "not imported — Hermes transcript format"}} {
		if info, err := os.Stat(filepath.Join(root, item.name)); err == nil && info.IsDir() {
			rows = append(rows, Row{ID: item.kind + ":" + item.name, Kind: item.kind, Name: item.name, Detail: item.detail})
		}
	}
	addFile("config:config.yaml", "config", "config.yaml", "config.yaml", "not imported — configure connections in Agent_b", false, false)
	addFile("auth:auth.json", "auth", "auth.json", "auth.json", "not imported — credentials follow in a later release", false, false)
	if len(rows) == 0 {
		return Preview{}, fmt.Errorf("%s does not look like a Hermes home", root)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return Preview{Rows: rows, SecretCount: len(secretNames)}, nil
}

func Import(source, profile, agentID string, maxTokens int, include []string) (Report, error) {
	preview, err := PreviewHome(source)
	if err != nil {
		return Report{}, err
	}
	wanted := map[string]bool{}
	for _, id := range include {
		wanted[id] = true
	}
	profile, err = filepath.Abs(profile)
	if err != nil {
		return Report{}, err
	}
	statePath := filepath.Join(profile, ".hermes-import.json")
	state := manifest{Files: map[string]record{}}
	if data, readErr := os.ReadFile(statePath); readErr == nil {
		_ = json.Unmarshal(data, &state)
	}
	if state.Files == nil {
		state.Files = map[string]record{}
	}
	report := Report{}
	memoryRows := []Row{}
	for _, row := range preview.Rows {
		if !wanted[row.ID] {
			continue
		}
		switch row.Kind {
		case "memory":
			memoryRows = append(memoryRows, row)
		case "skill":
			item, changed, copyErr := importSkill(source, profile, row, state.Files, &report)
			if copyErr != nil {
				return report, copyErr
			}
			report.Changed = report.Changed || changed
			report.Skills = append(report.Skills, item)
		}
	}
	if len(memoryRows) > 0 {
		changed, memoryErr := importMemory(source, profile, agentID, maxTokens, memoryRows, state.Files, &report)
		if memoryErr != nil {
			return report, memoryErr
		}
		report.Changed = report.Changed || changed
	}
	if report.Changed {
		data, _ := json.MarshalIndent(state, "", "  ")
		if err = writeWithin(profile, statePath, append(data, '\n')); err != nil {
			return report, err
		}
	}
	if report.Changed {
		report.Message = "Hermes import complete"
		if len(report.Skipped) == 0 {
			report.Message += "; the Hermes folder is no longer needed"
		}
	} else {
		report.Message = "nothing changed"
	}
	return report, nil
}

func importSkill(source, profile string, row Row, records map[string]record, report *Report) (ImportedSkill, bool, error) {
	destination := filepath.Join(profile, "skills", row.Name)
	changed := false
	err := filepath.WalkDir(row.source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Hermes skill contains a symbolic link: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		relative, _ := filepath.Rel(row.source, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if utf8.Valid(data) {
			text := rewrite(string(data), source, destination)
			if strings.EqualFold(relative, "SKILL.md") {
				text = replaceName(text, row.Name)
			}
			data = []byte(text)
		}
		target := filepath.Join(destination, relative)
		key := filepath.ToSlash(filepath.Join("skills", row.Name, relative))
		sourceHash, destinationHash := digest(data), fileDigest(target)
		old, known := records[key]
		if known && old.Source == sourceHash {
			if destinationHash == old.Destination {
				return nil
			}
			report.Skipped = append(report.Skipped, key+" edited since import")
			return nil
		}
		if known && destinationHash != "" && destinationHash != old.Destination {
			report.Skipped = append(report.Skipped, key+" edited since import")
			return nil
		}
		if err = writeWithin(profile, target, data); err != nil {
			return err
		}
		records[key] = record{Source: sourceHash, Destination: digest(data)}
		changed = true
		return nil
	})
	for _, call := range row.Calls {
		report.Unresolved = append(report.Unresolved, row.Name+":"+call)
	}
	if changed {
		report.Rewritten = append(report.Rewritten, row.Name)
	}
	return ImportedSkill{Name: row.Name, Enabled: len(row.Calls) == 0, Calls: row.Calls, Changed: changed}, changed, err
}

func importMemory(source, profile, agentID string, maxTokens int, rows []Row, records map[string]record, report *Report) (bool, error) {
	parts, sourceParts := []string{}, []string{}
	for _, row := range rows {
		data, err := os.ReadFile(row.source)
		if err != nil {
			return false, err
		}
		sourceParts = append(sourceParts, row.ID+":"+digest(data))
		parts = append(parts, rewrite(string(data), source, profile))
	}
	target := filepath.Join(profile, "memory", "agent-"+agentID+".md")
	current, err := os.ReadFile(target)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	sourceHash, currentHash := digest([]byte(strings.Join(sourceParts, "\n"))), digest(current)
	old, known := records["memory"]
	if known && old.Source == sourceHash {
		if currentHash == old.Destination {
			return false, nil
		}
		report.Skipped = append(report.Skipped, "memory edited since import")
		return false, nil
	}
	if known && currentHash != old.Destination {
		report.Skipped = append(report.Skipped, "memory edited since import")
		return false, nil
	}
	base := removeMemoryBlock(string(current))
	body := strings.TrimSpace(strings.Join(parts, "\n\n"))
	limit := maxTokens * 4
	header := "<!-- hermes-import:start -->\n## " + time.Now().Format("2006-01-02") + " imported from Hermes\n"
	footer := "\n<!-- hermes-import:end -->"
	available := limit - len([]rune(base)) - len([]rune(header+footer))
	if limit > 0 && len([]rune(body)) > available {
		if available < 0 {
			available = 0
		}
		body = tailRunes(body, available)
		for _, row := range rows {
			report.Cut = append(report.Cut, row.Name)
		}
	}
	next := strings.TrimSpace(base)
	if next != "" {
		next += "\n\n"
	}
	next += header + body + footer + "\n"
	if err = writeWithin(profile, target, []byte(next)); err != nil {
		return false, err
	}
	records["memory"] = record{Source: sourceHash, Destination: digest([]byte(next))}
	report.Rewritten = append(report.Rewritten, "memory")
	return true, nil
}

func writeWithin(root, target string, data []byte) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("import destination is outside the profile")
	}
	if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	for parent := filepath.Dir(target); parent != filepath.Dir(root); parent = filepath.Dir(parent) {
		if info, statErr := os.Lstat(parent); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("import destination contains a symbolic link")
		}
		if filepath.Clean(parent) == filepath.Clean(root) {
			break
		}
	}
	return os.WriteFile(target, data, 0o600)
}
func rewrite(text, source, destination string) string {
	for _, old := range []string{filepath.ToSlash(source), source, "$HERMES_HOME", "${HERMES_HOME}", "~/.hermes", "HERMES_HOME"} {
		text = strings.ReplaceAll(text, old, filepath.ToSlash(destination))
	}
	text = regexp.MustCompile(`skill_view\s*\([^)]*\)`).ReplaceAllString(text, "read_file(SKILL.md)")
	return regexp.MustCompile(`skills_list\s*\(\s*\)`).ReplaceAllString(text, "the ## Skills index in the system prompt")
}
func replaceName(text, name string) string {
	return regexp.MustCompile(`(?m)^name:\s*.*$`).ReplaceAllString(text, "name: "+name)
}
func slug(value string) string {
	return strings.Trim(invalidName.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-"), "-")
}
func metadata(text string) (string, string, []string) {
	name, description, environment := "", "", []string{}
	inEnvironment := false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		key, value, found := strings.Cut(trimmed, ":")
		if found {
			inEnvironment = key == "required_environment_variables"
			switch key {
			case "name":
				name = strings.Trim(strings.TrimSpace(value), `"'`)
			case "description":
				description = strings.Trim(strings.TrimSpace(value), `"'`)
			}
			continue
		}
		if inEnvironment && strings.HasPrefix(trimmed, "-") {
			environment = append(environment, strings.TrimSpace(strings.TrimPrefix(trimmed, "-")))
		}
	}
	return name, description, environment
}
func skillCalls(root string) []string {
	found := map[string]bool{}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		data, _ := os.ReadFile(path)
		if !utf8.Valid(data) {
			return nil
		}
		for lineNumber, line := range strings.Split(string(data), "\n") {
			for _, name := range unsupported {
				if regexp.MustCompile(`\b` + name + `\s*\(`).MatchString(line) {
					rel, _ := filepath.Rel(root, path)
					found[filepath.ToSlash(rel)+fmt.Sprintf(":%d %s", lineNumber+1, name)] = true
				}
			}
		}
		return nil
	})
	out := []string{}
	for value := range found {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
func envNames(path string) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	names := []string{}
	name := strings.Builder{}
	before, comment := true, false
	for {
		b, err := reader.ReadByte()
		if err != nil {
			if err == io.EOF {
				if name.Len() > 0 && !comment {
					names = append(names, strings.TrimSpace(name.String()))
				}
			}
			break
		}
		if b == '\n' || b == '\r' {
			if name.Len() > 0 && !comment {
				names = append(names, strings.TrimSpace(name.String()))
			}
			name.Reset()
			before = true
			comment = false
			continue
		}
		if before {
			if b == '#' {
				comment = true
			}
			if !comment && b == '=' {
				before = false
			} else if !comment {
				name.WriteByte(b)
			}
		}
	}
	sort.Strings(names)
	return names
}
func removeMemoryBlock(text string) string {
	start := strings.Index(text, "<!-- hermes-import:start -->")
	if start < 0 {
		return text
	}
	end := strings.Index(text[start:], "<!-- hermes-import:end -->")
	if end < 0 {
		return text
	}
	return strings.TrimSpace(text[:start] + text[start+end+len("<!-- hermes-import:end -->"):])
}
func tailRunes(text string, count int) string {
	runes := []rune(text)
	if count >= len(runes) {
		return text
	}
	if count <= 0 {
		return ""
	}
	return string(runes[len(runes)-count:])
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func fileDigest(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return digest(data)
}
