package agent

import (
	"os"
	"strings"
	"testing"
)

func TestOrdinaryRunHasNoPlanRegistrationCardPath(t *testing.T) {
	data, err := os.ReadFile("run_phases.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if strings.Contains(source, "handlePlanRegistration(") || strings.Contains(source, "handleModelPlanProposal(") {
		t.Fatal("ordinary run still invokes a plan registration card path")
	}
}
