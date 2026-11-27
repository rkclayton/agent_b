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

	delegate string
	folders  func() []config.TrustedFolder
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
	return r.RenderMemoryParts(connection, s, toolNames, s.ProjectBlock, memory)
}
func (r *PromptRenderer) RenderParts(connection *config.Connection, s *session.Session, toolNames []string, project, memory string) string {
	return r.RenderMemoryParts(connection, s, toolNames, project, memory, "")
}

// RenderMemoryParts takes the memory layers as a list rather than as two named
// arguments: added a third, `machine`, and a list means a layer joins the
// prompt without every caller having to be told about it.
func (r *PromptRenderer) RenderMemoryParts(connection *config.Connection, s *session.Session, toolNames []string, project string, layers ...string) string {
	return r.RenderSkillParts(connection, s, toolNames, project, "", layers...)
}

func (r *PromptRenderer) RenderSkillParts(connection *config.Connection, s *session.Session, toolNames []string, project, skills string, layers ...string) string {
	r.mu.RLock()
	template := r.text
	delegate := r.delegate
	folders := r.folders
	r.mu.RUnlock()
	if s.Role == "e" && delegate != "" {
		template = delegate
	}
	if connection.SystemPromptOverride != "" && s.Role != "e" {
		template = connection.SystemPromptOverride
	}
	agentBlock := ""
	if addendum := strings.TrimSpace(s.AgentPrompt()); addendum != "" {
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
	value = strings.ReplaceAll(value, "{{network_boundary}}", s.NetworkBoundary)
	value = strings.ReplaceAll(value, "{{media_capabilities}}", s.MediaCapabilities)
	value = strings.ReplaceAll(value, "{{tools}}", strings.Join(toolNames, ", "))
	value = strings.ReplaceAll(value, "{{agent}}", agentBlock)
	value = strings.ReplaceAll(value, "{{project}}", project)
	present := make([]string, 0, len(layers)+1)
	if trimmed := strings.TrimSpace(skills); trimmed != "" {
		present = append(present, trimmed)
	}
	if len(layers) > 0 {
		present = append(present, memoryPromptLine(layers[0]))
	}
	memory := strings.TrimSpace(strings.Join(present, "\n\n"))
	value = strings.ReplaceAll(value, "{{memory}}", memory)
	value = strings.ReplaceAll(value, "{{os_context}}", operatingSystemContext())
	value = strings.ReplaceAll(value, "{{date}}", time.Now().Format("2006-01-02"))
	for _, name := range toolNames {
		if name == "recall" {
			value = strings.TrimRight(value, "\r\n") + "\n\nEarlier turns remain in this chat's record; use recall to search or read them."
			break
		}
	}
	if memory == "" && project == "" {
		value = strings.TrimRight(value, "\r\n")
	}
	return value
}

func memoryPromptLine(folderMemory string) string {
	count := 0
	for _, line := range strings.Split(folderMemory, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			count++
		}
	}
	return fmt.Sprintf("This folder has %d saved notes; recall reads them.", count)
}
