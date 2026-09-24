package probe

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
)

func TestApplyTemplateFindingIncludesServerFailure(t *testing.T) {
	finding := applyTemplateFinding("apply-template", false, "", errors.New("Jinja Exception: No user query found in messages."), "")
	if !strings.Contains(finding, "apply-template: unavailable") || !strings.Contains(finding, "No user query found in messages.") {
		t.Fatalf("finding=%q", finding)
	}
}

func TestAccountingApplyTemplateShapesLive(t *testing.T) {
	baseURL := os.Getenv("AGENTB_ACCOUNTING_PROBE_URL")
	model := os.Getenv("AGENTB_ACCOUNTING_PROBE_MODEL")
	if baseURL == "" || model == "" {
		t.Skip("set AGENTB_ACCOUNTING_PROBE_URL and _MODEL for the read-only real-template probe")
	}
	connection := config.Connection{
		BaseURL: baseURL, Model: model, RequestTimeoutS: 120, ProbeMode: "minimal",
		Context:   config.Context{NCtx: 262144, ReserveOutput: 4096},
		Reasoning: config.Reasoning{Control: "chat_template_kwargs", Enabled: false},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	_, findings, err := Probe(ctx, &connection)
	if err != nil {
		t.Fatal(err)
	}
	shapeFindings := []string{}
	for _, finding := range findings {
		if strings.HasPrefix(finding, "apply-template shape ") {
			shapeFindings = append(shapeFindings, finding)
			t.Log(finding)
		}
	}
	joined := strings.Join(shapeFindings, "\n")
	if len(shapeFindings) != len(accountingApplyTemplateProbeShapes(nil)) || !strings.Contains(joined, "shape system: unavailable: apply-template HTTP 500") || !strings.Contains(joined, "No user query found in messages") || !strings.Contains(joined, "shape system,user: available") {
		t.Fatalf("shape findings:\n%s", joined)
	}
}

func TestAccountingApplyTemplateProbeShapesCoverBudgetFamilies(t *testing.T) {
	shapes := accountingApplyTemplateProbeShapes([]any{map[string]any{"type": "function"}})
	names := make([]string, 0, len(shapes))
	for _, shape := range shapes {
		names = append(names, shape.name)
	}
	joined := strings.Join(names, "|")
	for _, want := range []string{"system", "user", "system,user + tools", "system,user,assistant,user", "assistant(tool),tool"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("probe shapes %q omit %q", joined, want)
		}
	}
}
