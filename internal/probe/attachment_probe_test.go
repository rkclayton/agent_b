package probe

import (
	"context"
	"testing"

	"harness/internal/config"
)

func TestProbeOffReportsAssumedDocumentAndImageInput(t *testing.T) {
	connection := config.Defaults(t.TempDir()).Connections[0]
	connection.ProbeMode = "off"
	capabilities, findings, err := Probe(context.Background(), &connection)
	if err != nil {
		t.Fatal(err)
	}
	if !capabilities.DocumentInput || !capabilities.ImageInput || capabilities.Vision != config.VisionReadsImages {
		t.Fatalf("capabilities=%+v", capabilities)
	}
	if len(findings) == 0 {
		t.Fatal("probe recorded no findings")
	}
}
