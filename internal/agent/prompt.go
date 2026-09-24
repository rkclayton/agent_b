package agent

import (
	"fmt"
	"os"
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
	return r.RenderMemoryParts(connection, s, toolNames, s.ProjectBlock, memory, s.AgentMemoryBlock)
}
func (r *PromptRenderer) RenderParts(connection *config.Connection, s *session.Session, toolNames []string, project, memory string) string {
	return r.RenderMemoryParts(connection, s, toolNames, project, memory, "")
}
func (r *PromptRenderer) RenderMemoryParts(connection *config.Connection, s *session.Session, toolNames []string, project, workspaceMemory, agentMemory string) string {
	r.mu.RLock()
	template := r.text
	planner := r.planner
	r.mu.RUnlock()
	if connection.SystemPromptOverride != "" {
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
	value = strings.ReplaceAll(value, "{{plans}}", s.PlansRoot)
	value = strings.ReplaceAll(value, "{{network_boundary}}", s.NetworkBoundary)
	value = strings.ReplaceAll(value, "{{tools}}", strings.Join(toolNames, ", "))
	value = strings.ReplaceAll(value, "{{agent}}", agentBlock)
	value = strings.ReplaceAll(value, "{{project}}", project)
	memory := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(workspaceMemory), strings.TrimSpace(agentMemory)}, "\n\n"))
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
