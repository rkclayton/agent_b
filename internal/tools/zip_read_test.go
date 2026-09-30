package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/session"
	"harness/internal/skills"
)

func makeZip(path string, files map[string]string) {
	file, _ := os.Create(path)
	writer := zip.NewWriter(file)
	for name, body := range files {
		entry, _ := writer.Create(name)
		_, _ = entry.Write([]byte(body))
	}
	_ = writer.Close()
	_ = file.Close()
	if files["huge.txt"] == "1GB" {
		data, _ := os.ReadFile(path)
		at := bytes.LastIndex(data, []byte("PK\x01\x02"))
		binary.LittleEndian.PutUint32(data[at+24:], 1<<30)
		_ = os.WriteFile(path, data, 0o600)
	}
}
func TestReadZipSafetyHintsAndSkillImport(t *testing.T) {
	require := func(ok bool, format string, args ...any) {
		t.Helper()
		if !ok {
			t.Fatalf(format, args...)
		}
	}
	root := t.TempDir()
	archive := filepath.Join(root, "report-kit.zip")
	skill := "---\nname: report-kit\ndescription: Builds invented fixture reports.\n---\nProcedure.\n"
	makeZip(archive, map[string]string{"report-kit/SKILL.md": skill, "report-kit/scripts/build.ps1": "'fixture'", "report-kit/README.md": "fixture", "report-kit/openapi.json": "{}"})
	cfg := config.Defaults(root)
	read := NewReadFile(cfg.Tools.ReadFile)
	chat := &session.Session{Workspace: root, LastSeen: map[string]time.Time{}}
	listing, err := read.Call(context.Background(), chat, map[string]any{"path": archive})
	require(err == nil && strings.Count(listing, "report-kit/") == 4, "listing=%q err=%v", listing, err)
	body, err := read.Call(context.Background(), chat, map[string]any{"path": archive + "/report-kit/SKILL.md"})
	require(err == nil && strings.Contains(body, "Builds invented fixture reports"), "body=%q err=%v", body, err)
	makeZip(filepath.Join(root, "evil.zip"), map[string]string{"../../evil.txt": "no", "/absolute.txt": "no"})
	listing, err = read.Call(context.Background(), chat, map[string]any{"path": filepath.Join(root, "evil.zip")})
	require(err == nil && strings.Count(listing, "refused") == 2, "unsafe=%q err=%v", listing, err)
	makeZip(filepath.Join(root, "bomb.zip"), map[string]string{"huge.txt": "1GB"})
	files := map[string]string{}
	for i := 0; i < 10000; i++ {
		files[fmt.Sprintf("f/%05d", i)] = ""
	}
	makeZip(filepath.Join(root, "many.zip"), files)
	for _, refused := range []string{filepath.Join(root, "bomb.zip"), filepath.Join(root, "many.zip")} {
		_, err = read.Call(context.Background(), chat, map[string]any{"path": refused})
		require(err != nil && !strings.Contains(err.Error(), "\n"), "refusal=%v", err)
	}
	decision := outsideCommandDecision(`Get-Content "`+filepath.Join(root, "report-kit", "SKILL.md")+`"`, &session.Session{Workspace: t.TempDir()}, nil)
	require(strings.Contains(decision.missing, archive+string(filepath.Separator)+"report-kit"), "hint=%q", decision.missing)
	setting, err := skills.Import(filepath.Join(root, "skills"), archive, cfg.Tools.Attachments.MaxBytes)
	require(err == nil && !setting.Enabled, "import=%+v err=%v", setting, err)
	data, err := os.ReadFile(filepath.Join(root, "skills", "report-kit", "SKILL.md"))
	require(err == nil && string(data) == skill, "imported=%q err=%v", data, err)
}
