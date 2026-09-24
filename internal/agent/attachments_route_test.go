package agent

import (
	"testing"

	attachmentfile "harness/internal/attachment"
	"harness/internal/config"
)

// Item 2ch (v1.2.5): the operator chose the route. Sidecar by default; inline
// only under the threshold and only where the connection reads documents. An
// image has no second route and is unaffected.
func TestPDFInlinesOnlyUnderTheThreshold(t *testing.T) {
	SetInlineDocumentLimit(2 << 20)
	documentConnection := &config.Connection{AttachmentHandling: "native"}
	textConnection := &config.Connection{AttachmentHandling: "extract"}

	for _, item := range []struct {
		name       string
		connection *config.Connection
		kind       attachmentfile.Kind
		bytes      int64
		want       bool
	}{
		{"a small PDF on a document connection inlines", documentConnection, attachmentfile.PDF, 1 << 20, true},
		{"a PDF exactly at the threshold inlines", documentConnection, attachmentfile.PDF, 2 << 20, true},
		{"a PDF over the threshold does not", documentConnection, attachmentfile.PDF, (2 << 20) + 1, false},
		{"a small PDF on a text connection does not", textConnection, attachmentfile.PDF, 1 << 10, false},
		{"an image has no second route and is unaffected by size", documentConnection, attachmentfile.Image, 64 << 20, true},
	} {
		if got := nativeAttachmentAt(item.connection, item.kind, item.bytes); got != item.want {
			t.Fatalf("%s: got %v", item.name, got)
		}
	}
}

// The threshold is the operator's, not a constant.
func TestInlineThresholdIsConfigured(t *testing.T) {
	SetInlineDocumentLimit(4 << 10)
	defer SetInlineDocumentLimit(2 << 20)
	connection := &config.Connection{AttachmentHandling: "native"}
	if !nativeAttachmentAt(connection, attachmentfile.PDF, 4<<10) {
		t.Fatal("a PDF at the configured threshold must inline")
	}
	if nativeAttachmentAt(connection, attachmentfile.PDF, (4<<10)+1) {
		t.Fatal("a PDF over the configured threshold must not inline")
	}
}
