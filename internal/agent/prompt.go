package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"harness/internal/config"
	"harness/internal/session"
)

type PromptRenderer struct {
	mu         sync.RWMutex
	path, text string
	planner    string
	worker     string
	delegate   string
	folders    func() []config.TrustedFolder
}

func (r *PromptRenderer) SetTrustedFolders(folders func() []config.TrustedFolder) {
	r.mu.Lock()
	r.folders = folders
	r.mu.Unlock()
}

func (r *PromptRenderer) LoadDelegate(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("delegate prompt %s: %w", path, err)
	}
	r.mu.Lock()
	r.delegate = strings.TrimSpace(string(data))
	r.mu.Unlock()
	return nil
}

func (r *PromptRenderer) LoadPlanner(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("planner prompt %s: %w", path, err)
	}
	text := string(data)
	const start = "## Session prompt"
	if index := strings.Index(text, start); index >= 0 {
		text = text[index+len(start):]
		if end := strings.Index(text, "\n## "); end >= 0 {
			text = text[:end]
		}
	}
	r.mu.Lock()
	r.planner = strings.TrimSpace(text)
	r.mu.Unlock()
	return nil
}

func LoadTemplate(path string) (*PromptRenderer, error) {
	r := &PromptRenderer{path: path}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}
func (r *PromptRenderer) Reload() error {
	data, err := os.ReadFile(r.path)
	if err != nil {
		return fmt.Errorf("system prompt %s: %w", r.path, err)
	}
	r.mu.Lock()
	r.text = string(data)
	r.mu.Unlock()
	return nil
}
func (r *PromptRenderer) Render(connection *config.Connection, s *session.Session, toolNames []string, memory string) string {
	return r.RenderMemoryParts(connection, s, toolNames, s.ProjectBlock, memory, s.AgentMemoryBlock, s.MachineMemoryBlock)
}
func (r *PromptRenderer) RenderParts(connection *config.Connection, s *session.Session, toolNames []string, project, memory string) string {
	return r.RenderMemoryParts(connection, s, toolNames, project, memory, "")
}

// RenderMemoryParts takes the memory layers as a list rather than as two named
// arguments: item 2mw added a third, `machine`, and a list means a layer joins the
// prompt without every caller having to be told about it.
func (r *PromptRenderer) RenderMemoryParts(connection *config.Connection, s *session.Session, toolNames []string, project string, layers ...string) string {
	return r.RenderSkillParts(connection, s, toolNames, project, "", layers...)
}

func (r *PromptRenderer) RenderSkillParts(connection *config.Connection, s *session.Session, toolNames []string, project, skills string, layers ...string) string {
	r.mu.RLock()
	template := r.text
	planner := r.planner
	delegate := r.delegate
	folders := r.folders
	r.mu.RUnlock()
	if s.Role == "e" && delegate != "" {
		template, planner = delegate, ""
	}
	if connection.SystemPromptOverride != "" && s.Role != "e" {
		template = connection.SystemPromptOverride
	}
	agentBlock := ""
	if addendum := strings.TrimSpace(s.PromptAddendum); addendum != "" {
		agentBlock = "BEGIN OPERATOR AGENT ADDENDUM\n" + addendum + "\nEND OPERATOR AGENT ADDENDUM"
	}
	if !strings.Contains(template, "{{agent}}") && agentBlock != "" {
		if strings.Contains(template, "{{project}}") {
			template = strings.Replace(template, "{{project}}", "{{agent}}\n{{project}}", 1)
		} else {
			template = strings.TrimRight(template, "\r\n") + "\n{{agent}}\n"
		}
	}
	value := strings.ReplaceAll(template, "{{workspace}}", s.Workspace)
	value = strings.ReplaceAll(value, "{{folder}}", s.Workspace)
	folderText := "none yet"
	if folders != nil {
		entries := folders()
		if len(entries) > 0 {
			items := make([]string, 0, len(entries))
			for _, entry := range entries {
				items = append(items, filepath.Base(entry.Path)+" ("+entry.Path+")")
			}
			folderText = strings.Join(items, "; ")
		}
	}
	value = strings.ReplaceAll(value, "{{folders}}", folderText)
	value = strings.ReplaceAll(value, "{{plans}}", s.PlansRoot)
	value = strings.ReplaceAll(value, "{{network_boundary}}", s.NetworkBoundary)
	value = strings.ReplaceAll(value, "{{media_capabilities}}", s.MediaCapabilities)
	value = strings.ReplaceAll(value, "{{tools}}", strings.Join(toolNames, ", "))
	value = strings.ReplaceAll(value, "{{agent}}", agentBlock)
	value = strings.ReplaceAll(value, "{{project}}", project)
	present := make([]string, 0, len(layers)+1)
	if trimmed := strings.TrimSpace(skills); trimmed != "" {
		present = append(present, trimmed)
	}
	for _, layer := range layers {
		if trimmed := strings.TrimSpace(layer); trimmed != "" {
			present = append(present, trimmed)
		}
	}
	memory := strings.TrimSpace(strings.Join(present, "\n\n"))
	value = strings.ReplaceAll(value, "{{memory}}", memory)
	value = strings.ReplaceAll(value, "{{os_context}}", operatingSystemContext())
	value = strings.ReplaceAll(value, "{{date}}", time.Now().Format("2006-01-02"))
	if planner != "" && (s.Role == "d" || s.IsPlanPage()) {
		value = strings.TrimRight(value, "\r\n") + "\n\n" + planner
	}
	// The worker is told what it is doing, not how the product works: its whole
	// brief is the item in front of it, and it replaces the planner block rather
	// than stacking with it.
	if s.Role == "c" && r.worker != "" {
		value = strings.TrimRight(value, "\r\n") + "\n\n" + r.renderWorker(s)
	}
	if memory == "" && project == "" {
		value = strings.TrimRight(value, "\r\n")
	}
	return value
}
