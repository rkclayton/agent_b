package telemetry

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Item 2jg (a): a test fails if an event type exists in the product that the
// filter has not classified as sent-with-fields or dropped.
//
// This is the whole defensibility of an opt-out. A deny-list lets a new event
// type leak by default; an allow-list with an incomplete classification lets one
// vanish from the conversation entirely, which is the state item 2lq found in a
// bench list and this order exists to stop repeating.
func TestEveryProductEventTypeIsClassified2jg(t *testing.T) {
	product := productEventTypes(t)
	if len(product) < 40 {
		t.Fatalf("only %d event types read from internal/events; the source of truth moved", len(product))
	}
	for _, name := range product {
		if _, known := Classify(name); !known {
			t.Errorf("event type %q is emitted by the product and the allow-list has not classified it — add it to docs/TELEMETRY.md and to allowList, as sent-with-fields or as dropped", name)
		}
	}
	// And the reverse: a classification for a type the product no longer emits is
	// a stale line that will outlive its subject.
	known := map[string]bool{}
	for _, name := range product {
		known[name] = true
	}
	for _, name := range ClassifiedTypes() {
		if !known[name] {
			t.Errorf("the allow-list classifies %q, which the product no longer emits", name)
		}
	}
}

// productEventTypes reads the event names from internal/events, which is where
// they are declared. Reading the declaration rather than a copy is the point: a
// copy would agree with itself forever.
func productEventTypes(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "events", "event.go"))
	if err != nil {
		t.Fatal(err)
	}
	// Each is `Name  = "some.type"` or `Name = "some.type"` in the const blocks.
	// Not every name is dotted: error, stage, budget, compaction and speech are
	// single words, and a pattern that required a dot reported all five as types
	// the product no longer emits.
	pattern := regexp.MustCompile(`^\s*[A-Z][A-Za-z]*\s*=\s*"([a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*)"`)
	seen := map[string]bool{}
	out := []string{}
	for _, line := range strings.Split(string(body), "\n") {
		match := pattern.FindStringSubmatch(line)
		if match == nil || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		out = append(out, match[1])
	}
	return out
}

// The negative control for the test above: a type nobody has classified is
// caught, which is what makes the completeness claim worth anything.
func TestAnUnclassifiedTypeIsNotSilentlyDropped2jg(t *testing.T) {
	if _, known := Classify("scratch.invented_for_this_test"); known {
		t.Fatal("an invented type was classified, so the completeness test cannot fail")
	}
	// It is also not SENT, which is the safe half of the same answer.
	class, _ := Classify("scratch.invented_for_this_test")
	if class.Sent {
		t.Fatal("an unclassified type would be sent")
	}
}

// Item 2jg (b): redaction before send. The fixture carries a path, a URL, an
// email and a token, which is the acceptance line's own list.
func TestRedactionRemovesEverythingTheContractForbids2jg(t *testing.T) {
	for _, probe := range []struct {
		name, in string
		gone     []string
		want     string
	}{
		{"a windows path", `open C:\work\acme\AppData\Local\Temp\secret.txt failed`, []string{"acme", "AppData", "secret"}, "<path>"},
		{"a UNC path", `\\fileserver\share\payroll.xlsx is locked`, []string{"fileserver", "payroll"}, "<path>"},
		{"a URL", `GET https://ai.acmeholding.example/v1/chat returned 500`, []string{"acmeholding", "/v1/chat"}, "<host>"},
		{"a bare host", `dial tcp ai.acmeholding.example:443: refused`, []string{"acmeholding"}, "<host>"},
		{"an address", `dial tcp 192.168.1.10:8080: refused`, []string{"192.168.1.10"}, "<host>"},
		{"an email", `notify someone@example.org failed`, []string{"acme", "gmail"}, "<email>"},
		{"a token", `Authorization: Bearer sk-abcdefghijklmnopqrstuvwxyz0123456789`, []string{"sk-abcdefghijklmnop"}, "<token>"},
	} {
		got := Redact(probe.in)
		for _, forbidden := range probe.gone {
			if strings.Contains(got, forbidden) {
				t.Errorf("%s: %q survived redaction of %q", probe.name, forbidden, probe.in)
			}
		}
		if !strings.Contains(got, probe.want) {
			t.Errorf("%s: %q should contain %s, got %q", probe.name, probe.in, probe.want, got)
		}
	}
}

func TestRedactionCutsLongStrings2jg(t *testing.T) {
	long := strings.Repeat("a b ", 200)
	if got := Redact(long); len(got) > MaxStringLength {
		t.Fatalf("a %d-character string survived at %d characters", len(long), len(got))
	}
}

// Item 2ji's narrowing survives the filter: a bucket the server could not supply
// is ABSENT on the wire, and a receiver cannot tell a measured zero from a
// missing one if the filter fills it in.
func TestPickLeavesAnAbsentFieldAbsent2jg(t *testing.T) {
	class, _ := Classify("run.stopped")
	picked := Pick(class, map[string]any{
		"reason": "done", "turns": 4,
		"total_ms": 1000, "model_ms": 900, "tool_ms": 0,
		"model_class": "local",
	})
	if _, present := picked["prompt_ms"]; present {
		t.Error("prompt_ms was absent in the event and the filter invented it")
	}
	if got, ok := picked["tool_ms"]; !ok || got != 0 {
		t.Errorf("a MEASURED zero must survive: %v", picked["tool_ms"])
	}
	// And nothing outside the allow-list rides along.
	for _, forbidden := range []string{"run_id", "detail", "armed_detectors", "queue_held", "terminal_reason"} {
		if _, present := picked[forbidden]; present {
			t.Errorf("%s left the machine", forbidden)
		}
	}
}

func TestPickDropsEverythingFromADroppedType2jg(t *testing.T) {
	class, known := Classify("model.response")
	if !known {
		t.Fatal("model.response is unclassified")
	}
	if class.Sent {
		t.Fatal("model.response is marked sent; it carries the model's words")
	}
	if got := Pick(class, map[string]any{"content": "the model said this"}); got != nil {
		t.Fatalf("a dropped type produced %v", got)
	}
}

func validCrashTree2p7() map[string]any {
	return map[string]any{
		"binaries": []any{map[string]any{
			"uuid": "01234567-89ab-cdef-0123-456789abcdef", "name": "Agent_b.exe", "text_offset": 0,
		}},
		"threads": []any{map[string]any{
			"frames": []any{map[string]any{"binary": 0, "offset": 4660, "address": 140700000001234}},
		}},
		"exception_type": 0, "signal": 0, "termination_reason": "go.panic", "truncated": false,
	}
}

func TestCrashTreeHasAClosedContentFreeShape2p7(t *testing.T) {
	class, _ := Classify("error")
	good := Pick(class, map[string]any{"where": "host_window", "class": "crash", "stack_tree": validCrashTree2p7()})
	if good["stack_tree"] == nil {
		t.Fatal("a bounded crash tree was dropped")
	}
	for name, mutate := range map[string]func(map[string]any){
		"free text":        func(tree map[string]any) { tree["message"] = "C:\work\acme\\secret.go panic" },
		"too many threads": func(tree map[string]any) { tree["threads"] = make([]any, 17) },
		"bad reason":       func(tree map[string]any) { tree["termination_reason"] = "the panic message" },
	} {
		t.Run(name, func(t *testing.T) {
			tree := validCrashTree2p7()
			mutate(tree)
			if got := Pick(class, map[string]any{"where": "runtime", "class": "crash", "stack_tree": tree}); got != nil {
				t.Fatalf("invalid crash event survived: %#v", got)
			}
		})
	}
}

// docs/TELEMETRY.md is the contract and the allow-list is generated against it,
// so the two agreeing is not decoration: a field added to one and not the other
// is exactly how a document stops describing the product.
func TestTheDocumentAndTheAllowListAgree2jg(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "TELEMETRY.md"))
	if err != nil {
		t.Fatal(err)
	}
	document := string(body)
	for _, name := range ClassifiedTypes() {
		if !strings.Contains(document, name) {
			t.Errorf("the allow-list classifies %q and docs/TELEMETRY.md does not mention it", name)
		}
	}
	// Every field the allow-list would send must be named in the document too.
	for _, name := range ClassifiedTypes() {
		class, _ := Classify(name)
		if !class.Sent {
			continue
		}
		for _, field := range class.Fields {
			if !strings.Contains(document, field) {
				t.Errorf("%s.%s would be sent and docs/TELEMETRY.md does not name it", name, field)
			}
		}
	}
}
