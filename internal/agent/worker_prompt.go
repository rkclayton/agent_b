package agent

import (
	"fmt"
	"os"
	"strings"

	"harness/internal/session"
)

// LoadWorker reads prompts/worker.md, the brief a c-role session runs under. It
// is loaded the way the planner prompt is, so an install without the file simply
// has no worker brief rather than failing to start.
func (r *PromptRenderer) LoadWorker(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("worker prompt %s: %w", path, err)
	}
	r.mu.Lock()
	r.worker = strings.TrimSpace(string(data))
	r.mu.Unlock()
	return nil
}

// renderWorker fills the brief with the item the worker is on. The fields come
// from the item itself; an absent field renders as "(not stated)" rather than
// leaving a placeholder in the prompt, because a literal {{item_acceptance}} in
// front of a model is worse than an honest blank.
func (r *PromptRenderer) renderWorker(s *session.Session) string {
	job := s.WorkerJob()
	value := r.worker
	for placeholder, field := range map[string]string{
		"{{item_intent}}":     job.Intent,
		"{{item_approach}}":   job.Approach,
		"{{item_acceptance}}": job.Acceptance,
		"{{item_negative}}":   job.Negative,
		"{{plan_repo}}":       job.Repo,
	} {
		text := strings.TrimSpace(field)
		if text == "" {
			text = "(not stated)"
		}
		value = strings.ReplaceAll(value, placeholder, text)
	}
	return value
}
