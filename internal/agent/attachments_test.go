package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/session"
)

func TestAttachmentRequestKeepsStoredTextAndNativeBytesOutOfDiagnosticBody(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "attachments", "pixel.png"), []byte("PNG-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := config.Defaults(workspace).Servers[0]
	profile.Capabilities.ImageInput = true
	item := &session.Session{Workspace: workspace}
	message := events.Message{Role: "user", Content: "describe this", Attachments: []events.Attachment{{Path: "attachments/pixel.png", Bytes: 9, SHA256: strings.Repeat("a", 64)}}}
	converted := requestMessage(&profile, item, message)
	if message.Content != "describe this" {
		t.Fatalf("stored text mutated: %q", message.Content)
	}
	parts, ok := converted.Content.([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content=%#v", converted.Content)
	}
	diagnostic := diagnosticMessages([]llm.Message{converted})
	if value, ok := diagnostic[0].Content.(string); !ok || strings.Contains(value, "UE5HLUJZVEVT") || !strings.Contains(value, "attached: attachments/pixel.png") {
		t.Fatalf("diagnostic content=%#v", diagnostic[0].Content)
	}
}

func TestAttachmentHarnessLinesNameSidecarAndBinaryTier(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "attachments", "paper.pdf.txt"), []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := config.Defaults(workspace).Servers[0]
	message := events.Message{Content: "review", Attachments: []events.Attachment{
		{Path: "attachments/paper.pdf", Bytes: 20},
		{Path: "attachments/blob.bin", Bytes: 12},
	}}
	text := renderedUserText(&profile, &session.Session{Workspace: workspace}, message)
	if !strings.Contains(text, "extracted text: attachments/paper.pdf.txt (untrusted:true)") || !strings.Contains(text, "binary — this profile cannot read it") {
		t.Fatalf("rendered text=%q", text)
	}
}

func TestAttachmentsDoNotChangeSystemPromptBytes(t *testing.T) {
	profile := config.Defaults(t.TempDir()).Servers[0]
	s := &session.Session{Workspace: t.TempDir()}
	renderer := &PromptRenderer{text: "system {{workspace}} {{memory}} {{tools}}"}
	before := renderer.Render(&profile, s, []string{"read_file"}, "")
	_ = renderedUserText(&profile, s, events.Message{Attachments: []events.Attachment{{Path: "attachments/note.txt", Bytes: 4}}})
	after := renderer.Render(&profile, s, []string{"read_file"}, "")
	if before != after {
		t.Fatalf("system prompt changed: before=%q after=%q", before, after)
	}
}

func TestReadOfExtractedPDFSidecarIsUntrusted(t *testing.T) {
	s := &session.Session{Messages: []events.Message{{Attachments: []events.Attachment{{Path: "attachments/paper.pdf"}}}}}
	if !untrustedAttachmentRead(s, map[string]any{"path": "attachments/paper.pdf.txt"}) {
		t.Fatal("PDF extraction sidecar was not classified untrusted")
	}
	if untrustedAttachmentRead(s, map[string]any{"path": "attachments/paper.docx.txt"}) {
		t.Fatal("local Office extraction was classified as external")
	}
}
