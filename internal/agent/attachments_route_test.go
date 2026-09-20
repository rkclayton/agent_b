package agent

import (
	"testing"

	attachmentfile "harness/internal/attachment"
	"harness/internal/config"
)

// Item 2ch (v1.2.5): the operator chose the route. Sidecar by default; inline
// only under the threshold and only where the profile reads documents. An
// image has no second route and is unaffected.
func TestPDFInlinesOnlyUnderTheThreshold(t *testing.T) {
	SetInlineDocumentLimit(2 << 20)
	documentProfile := &config.Profile{AttachmentHandling: "native"}
	textProfile := &config.Profile{AttachmentHandling: "extract"}

	for _, item := range []struct {
		name    string
		profile *config.Profile
		kind    attachmentfile.Kind
		bytes   int64
		want    bool
	}{
		{"a small PDF on a document profile inlines", documentProfile, attachmentfile.PDF, 1 << 20, true},
		{"a PDF exactly at the threshold inlines", documentProfile, attachmentfile.PDF, 2 << 20, true},
		{"a PDF over the threshold does not", documentProfile, attachmentfile.PDF, (2 << 20) + 1, false},
		{"a small PDF on a text profile does not", textProfile, attachmentfile.PDF, 1 << 10, false},
		{"an image has no second route and is unaffected by size", documentProfile, attachmentfile.Image, 64 << 20, true},
	} {
		if got := nativeAttachmentAt(item.profile, item.kind, item.bytes); got != item.want {
			t.Fatalf("%s: got %v", item.name, got)
		}
	}
}

// The threshold is the operator's, not a constant.
func TestInlineThresholdIsConfigured(t *testing.T) {
	SetInlineDocumentLimit(4 << 10)
	defer SetInlineDocumentLimit(2 << 20)
	profile := &config.Profile{AttachmentHandling: "native"}
	if !nativeAttachmentAt(profile, attachmentfile.PDF, 4<<10) {
		t.Fatal("a PDF at the configured threshold must inline")
	}
	if nativeAttachmentAt(profile, attachmentfile.PDF, (4<<10)+1) {
		t.Fatal("a PDF over the configured threshold must not inline")
	}
}
