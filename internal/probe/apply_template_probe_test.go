package probe

import (
	"errors"
	"strings"
	"testing"
)

func TestApplyTemplateFindingIncludesServerFailure(t *testing.T) {
	finding := applyTemplateFinding("apply-template", false, "", errors.New("Jinja Exception: No user query found in messages."), "")
	if !strings.Contains(finding, "apply-template: unavailable") || !strings.Contains(finding, "No user query found in messages.") {
		t.Fatalf("finding=%q", finding)
	}
}
