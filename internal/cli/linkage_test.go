package cli_test

import (
	"os/exec"
	"strings"
	"testing"
)

// Item 2iz (d): the engine's independence is ENFORCED, not hoped.
//
// `agentb` is only worth having if it is the engine without the app. That claim
// decays the moment somebody adds a convenient import, and it decays silently:
// the binary still builds, still runs, and has quietly grown a web server's
// worth of linked code. So the claim is a test.
//
// rel-1.18.0/W0 measured the starting position before a line was written:
// internal/agent, internal/tools, internal/llm and internal/session pull in
// thirteen harness packages and NOT ONE of the excluded ones. The separation
// substantially already existed; this keeps it.
func TestAgentbLinksTheEngineAndNotTheApp2iz(t *testing.T) {
	excluded := []string{
		"harness/internal/web",
		"harness/internal/reflection",
		"harness/internal/updater",
		"harness/internal/signing",
		"harness/internal/notifications",
	}
	// internal/delivery is item 2iz (d)'s sixth exclusion and it does NOT hold.
	// It is not an app dependency the CLI reached for: internal/agent's own run
	// loop uses delivery.Source and delivery.Result as TYPES -- r.deliver's
	// signature, the produced-files map, workspaceFileChanges -- so anything that
	// links the engine links delivery. Severing it means moving those types out
	// of the run loop, which is a refactor of the run loop and is not this item.
	//
	// The item's @consequence-if-false is blocking only for internal/web, which
	// is NOT linked; this is the "narrowing elsewhere" half, taken deliberately
	// and recorded here rather than by quietly shortening the list. The exception
	// is pinned so it cannot widen: delivery may be linked, and the moment the
	// run loop stops needing it this assertion fails and the line comes out.
	const deliveryException = "harness/internal/delivery"
	deps := dependenciesOf(t, "harness/cmd/agentb")
	if len(deps) == 0 {
		t.Fatal("go list returned no dependencies; the check would pass vacuously")
	}
	linked := map[string]bool{}
	for _, name := range deps {
		linked[name] = true
	}
	for _, name := range excluded {
		if linked[name] {
			t.Errorf("cmd/agentb links %s — the engine is supposed to be reachable without the app, and an import is how that stops being true", name)
		}
	}
	// The positive control. Without it, a typo in the module path would make this
	// pass while checking nothing at all.
	for _, required := range []string{"harness/internal/agent", "harness/internal/tools", "harness/internal/session"} {
		if !linked[required] {
			t.Errorf("cmd/agentb does not link %s, so this test is not looking at the binary it thinks it is", required)
		}
	}
	// And the app still links what the app needs, which is what makes the
	// exclusion above a separation rather than a deletion.
	app := dependenciesOf(t, "harness/cmd/harness")
	appLinked := map[string]bool{}
	for _, name := range app {
		appLinked[name] = true
	}
	for _, name := range append(excluded, deliveryException) {
		if !appLinked[name] {
			t.Errorf("cmd/harness does not link %s either, so cmd/agentb excluding it proves nothing", name)
		}
	}
	// The exception, pinned in both directions.
	if !linked[deliveryException] {
		t.Errorf("cmd/agentb no longer links %s — the run loop has stopped needing it, so delete this exception and put the package back in the excluded list", deliveryException)
	}
	t.Logf("cmd/agentb links %d harness packages; cmd/harness links %d; the one documented exception is %s",
		count(deps), count(app), deliveryException)
}

func dependenciesOf(t *testing.T, target string) []string {
	t.Helper()
	command := exec.Command("go", "list", "-deps", target)
	command.Dir = ".."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", target, err, output)
	}
	out := []string{}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "harness/") {
			out = append(out, line)
		}
	}
	return out
}

func count(values []string) int { return len(values) }
