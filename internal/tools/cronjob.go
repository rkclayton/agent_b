package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"harness/internal/cron"
	"harness/internal/session"
)

type Cronjob struct{ manager *cron.Manager }

func NewCronjob(manager *cron.Manager) *Cronjob { return &Cronjob{manager: manager} }
func (*Cronjob) Name() string                   { return "cronjob" }
func (*Cronjob) Description() string {
	return "Create, list, update, pause, resume, run, or remove scheduled jobs. Times use the PC's local time. Scheduled runs cannot schedule other jobs."
}
func (*Cronjob) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"action":   map[string]any{"type": "string", "enum": []string{"create", "list", "update", "pause", "resume", "run", "remove"}},
		"schedule": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"},
		"skills": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "skill": map[string]any{"type": "string"},
		"repeat": map[string]any{"type": "integer", "minimum": 1}, "job_id": map[string]any{"type": "string"}, "deliver": map[string]any{"type": "string"},
	}, "required": []string{"action"}}
}
func (c *Cronjob) Call(ctx context.Context, item *session.Session, raw map[string]any) (string, error) {
	if item != nil && item.Origin == "scheduled" {
		return "", errors.New("a scheduled run cannot schedule another job")
	}
	if c.manager == nil {
		return "", errors.New("scheduler is unavailable")
	}
	if action, _ := raw["action"].(string); action == "run" {
		ref, _ := raw["job_id"].(string)
		job, err := c.manager.StartNow(ref)
		if err != nil {
			return "", err
		}
		data, _ := json.Marshal(cron.Reply{Job: job, Message: "started " + job.Name})
		return string(data), nil
	}
	a := cron.Args{}
	a.Action, _ = raw["action"].(string)
	a.Schedule, _ = raw["schedule"].(string)
	a.Prompt, _ = raw["prompt"].(string)
	a.Name, _ = raw["name"].(string)
	a.Skill, _ = raw["skill"].(string)
	a.JobID, _ = raw["job_id"].(string)
	a.Deliver, _ = raw["deliver"].(string)
	if n, ok := raw["repeat"].(float64); ok {
		a.Repeat = int(n)
	} else if n, ok := raw["repeat"].(int); ok {
		a.Repeat = n
	}
	if values, ok := raw["skills"].([]any); ok {
		for _, v := range values {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				a.Skills = append(a.Skills, s)
			}
		}
	} else if values, ok := raw["skills"].([]string); ok {
		a.Skills = append(a.Skills, values...)
	}
	reply, err := c.manager.Apply(ctx, a)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(reply)
	if err != nil {
		return "", fmt.Errorf("encode cronjob result: %w", err)
	}
	return string(data), nil
}
