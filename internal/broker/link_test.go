package broker

import (
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Item 2ns (a): THE PAIRING LINK IS DEFINED ONCE, and this is the gate that says the
// document and the code agree. The operator's words are why it exists at all: "this code
// is waaay too long its insane", "we dont need to have them enter a code do we?" — the
// code is unchanged, and what changed is that he never types it.

func TestThePairingLinkIsTheDocumentsLink2ns(t *testing.T) {
	agent := Identity{SigningSeed: make([]byte, 32), Agreement: make([]byte, 32)}
	for index := range agent.SigningSeed {
		agent.SigningSeed[index] = byte(index)
		agent.Agreement[index] = byte(index + 64)
	}
	code := "ABCDE-FGHJK-MNPQR-STVWX-YZ012-34567-89ABC-DEFGH"
	link := PairingLink(code, agent.SigningPublic())

	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("the link is not a URL: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "agentb.app" || parsed.Path != "/pair" {
		t.Fatalf("the link is %s://%s%s", parsed.Scheme, parsed.Host, parsed.Path)
	}
	// BOTH VALUES IN THE FRAGMENT. A fragment never reaches a server, which is the whole
	// reason for it: a phone with no app that opens this link tells agentb.app nothing.
	if parsed.RawQuery != "" {
		t.Fatalf("the link carries a query string, which a server would receive: %q", parsed.RawQuery)
	}
	fields := map[string]string{}
	for _, pair := range strings.Split(parsed.Fragment, "&") {
		name, value, found := strings.Cut(pair, "=")
		if !found {
			t.Fatalf("fragment field %q has no value", pair)
		}
		fields[name] = value
	}
	if fields["c"] != code {
		t.Errorf("the link changed the code: %q", fields["c"])
	}
	key, err := base64.RawURLEncoding.DecodeString(fields["k"])
	if err != nil {
		t.Fatalf("the key is not unpadded base64url: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("the key is %d bytes, want 32", len(key))
	}
	if string(key) != string(agent.SigningPublic()) {
		t.Error("the link does not carry this AgentB's Ed25519 identity key")
	}

	// And reading one back is the document's own rule, including that an unknown field
	// is ignored rather than refused — which is what lets the version stay 1.
	readCode, readKey, err := ReadPairingLink(link + "&x=later")
	if err != nil {
		t.Fatalf("a link with an unknown field was refused: %v", err)
	}
	if readCode != code || string(readKey) != string(agent.SigningPublic()) {
		t.Fatalf("the link did not round trip: %q", readCode)
	}
	for _, bad := range []string{
		"http://agentb.app/pair#c=" + code + "&k=" + fields["k"],
		"https://example.test/pair#c=" + code + "&k=" + fields["k"],
		"https://agentb.app/other#c=" + code + "&k=" + fields["k"],
		"https://agentb.app/pair#c=" + code,
		"https://agentb.app/pair#k=" + fields["k"],
		"https://agentb.app/pair#c=" + code + "&k=tooshort",
	} {
		if _, _, err := ReadPairingLink(bad); err == nil {
			t.Errorf("a reader accepted %q", bad)
		}
	}
}

// (a): the document exists and says what the code encodes.
func TestTheLinkDocumentMatchesTheCode2ns(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "pairing-link-v1.md"))
	if err != nil {
		t.Fatalf("docs/pairing-link-v1.md is missing: %v", err)
	}
	document := string(raw)
	for _, wanted := range []string{
		"https://agentb.app/pair#c=<code>&k=<key>",
		"fragment",
		"32 bytes",
		"base64url",
		"never stored",
		"never logged",
	} {
		if !strings.Contains(document, wanted) {
			t.Errorf("the document does not state %q", wanted)
		}
	}
	// The shape the document prints is the shape the code builds.
	agent := Identity{SigningSeed: make([]byte, 32), Agreement: make([]byte, 32)}
	link := PairingLink("CODE", agent.SigningPublic())
	template := strings.ReplaceAll(strings.ReplaceAll(link, "CODE", "<code>"), base64.RawURLEncoding.EncodeToString(agent.SigningPublic()), "<key>")
	if !strings.Contains(document, template) {
		t.Fatalf("the document does not carry the shape the code builds: %s", template)
	}
}
