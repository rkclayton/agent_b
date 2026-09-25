package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"harness/internal/config"
	"harness/internal/delivery"
	"harness/internal/session"
	"harness/internal/tools"
)

func TestWorkspaceFileChangesIncludesCreatedAndModifiedFiles(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep.txt")
	changed := filepath.Join(root, "changed.txt")
	if err := os.WriteFile(keep, []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(changed, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := workspaceFileSnapshot(root)
	if err := os.WriteFile(changed, []byte("new and longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "created.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := workspaceFileChanges(root, before)
	if len(got) != 2 || got["changed.txt"].Path != "changed.txt" || got["created.txt"].Path != "created.txt" {
		t.Fatalf("changes=%#v", got)
	}
}

func TestRunScriptRasterReachesExistingDeliveryPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("System.Drawing raster probe is Windows-specific")
	}
	workspace, exchange := t.TempDir(), t.TempDir()
	cfg := config.Defaults(workspace)
	cfg.Shell.ServiceAccount.Enabled = false
	cfg.Deliver.Mode = config.DeliverModeFolder
	cfg.Deliver.ExchangeFolder = exchange
	shell := tools.NewShell(cfg.Shell)
	shell.Configure(cfg)
	before := workspaceFileSnapshot(workspace)
	source := `Add-Type -AssemblyName System.Drawing
$bitmap = New-Object System.Drawing.Bitmap 3,2
$bitmap.SetPixel(1,1,[System.Drawing.Color]::FromArgb(12,34,56))
$bitmap.Save((Join-Path (Get-Location) 'render.png'), [System.Drawing.Imaging.ImageFormat]::Png)
$bitmap.Dispose()`
	item := &session.Session{ID: "raster", Workspace: workspace}
	if detail := tools.NewRunScript(shell).CallDetailed(context.Background(), item, map[string]any{"language": "powershell", "source": source}); detail.Err != nil {
		t.Fatal(detail.Err)
	}
	changes := workspaceFileChanges(workspace, before)
	created, ok := changes["render.png"]
	if !ok || created.Bytes == 0 {
		t.Fatalf("workspace changes=%#v", changes)
	}
	result := delivery.New(nil, func() config.Config { return cfg }).Deliver(item, "raster-run", delivery.SortedSources(changes))
	if len(result.Items) != 1 || result.Items[0].SourcePath != "render.png" || result.Items[0].Status != "copied" {
		t.Fatalf("delivery=%+v", result)
	}
	data, err := os.ReadFile(result.Items[0].DeliveredPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("delivered raster is not PNG: % x", data)
	}
}

func TestMessageLimitErrorExtractsServerSentence(t *testing.T) {
	limit, sentence, ok := messageLimitError(assertError(`chat stream HTTP 400: {"error":{"message":"conversation too long: 61 messages (limit 60)"}}`))
	if !ok || limit != 60 || sentence != "conversation too long: 61 messages (limit 60)" {
		t.Fatalf("limit=%d sentence=%q ok=%t", limit, sentence, ok)
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }
