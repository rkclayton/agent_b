package session

import (
	"errors"
	"path/filepath"
	"testing"

	"harness/internal/events"
)

func TestPlanRegistrationGrantsRepositoryBeforePublishing2jz(t *testing.T) {
	registry := NewRegistry(events.NewBus(), nil, nil, 1, nil)
	registry.SetPlansRoot(filepath.Join(t.TempDir(), "plans"))
	repository := t.TempDir()
	var granted string
	registry.SetPlanGrant(func(path string) error { granted = path; return nil })
	plan, created, err := registry.EnsurePlan(repository)
	if err != nil || !created || plan.Repo == "" || granted != plan.Repo {
		t.Fatalf("plan=%+v created=%t granted=%q err=%v", plan, created, granted, err)
	}

	refused := NewRegistry(events.NewBus(), nil, nil, 1, nil)
	refused.SetPlansRoot(filepath.Join(t.TempDir(), "plans"))
	refused.SetPlanGrant(func(string) error { return errors.New("ACL grant failed") })
	if _, _, err := refused.EnsurePlan(t.TempDir()); err == nil || err.Error() != "ACL grant failed" {
		t.Fatalf("registration did not fail with its ACL grant: %v", err)
	}
}
