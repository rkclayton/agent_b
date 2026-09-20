package reflection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Item 17-i, v1.1.0/W3. Three tiers, offline, never modifying the tree, each
// reporting which one produced the graph.
func TestStructureReportsTierOneForThisRepository(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	before := treeFingerprint(t, filepath.Join(root, "internal", "reflection"))
	graph, err := Structure(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Tier != TierResolver || !strings.Contains(graph.TierNote, "go list") {
		t.Fatalf("tier = %q (%s)", graph.Tier, graph.TierNote)
	}
	if len(graph.Nodes) < 20 || len(graph.Edges) < 20 {
		t.Fatalf("%d nodes, %d edges", len(graph.Nodes), len(graph.Edges))
	}
	found := false
	for _, node := range graph.Nodes {
		if node.Name == "harness/internal/reflection" {
			found = true
			if node.Files == 0 || node.Bytes == 0 {
				t.Fatalf("node = %+v", node)
			}
		}
		if strings.HasPrefix(node.Name, "modernc.org/") || strings.HasPrefix(node.Name, "github.com/") {
			t.Fatalf("a dependency outside the repository is in the graph: %q", node.Name)
		}
	}
	if !found {
		t.Fatal("this package is not in the graph")
	}
	if after := treeFingerprint(t, filepath.Join(root, "internal", "reflection")); after != before {
		t.Fatal("extraction modified the tree")
	}
	if text := graph.Text(5); len(scanLines(text)) != 8 || !strings.Contains(text, "tier 1") {
		t.Fatalf("text = %q", text)
	}
}

func TestStructureFallsToTierTwoForAJavaScriptFixtureAndTierThreeForABareTree(t *testing.T) {
	js := t.TempDir()
	write(t, filepath.Join(js, "index.js"), "import { helper } from './lib/helper.js';\nconst fs = require('node:fs');\n")
	write(t, filepath.Join(js, "lib", "helper.js"), "export const helper = 1;\n")
	write(t, filepath.Join(js, "lib", "unused.ts"), "import './helper.js';\n")
	graph, err := Structure(t.Context(), js)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Tier != TierImports || !strings.Contains(graph.TierNote, "JavaScript") {
		t.Fatalf("tier = %q (%s)", graph.Tier, graph.TierNote)
	}
	edges := map[string]string{}
	for _, edge := range graph.Edges {
		edges[edge.From] = edge.To
	}
	if edges["index.js"] != "lib/helper.js" {
		t.Fatalf("edges = %+v", graph.Edges)
	}
	if edges["lib/unused.ts"] != "lib/helper.js" {
		t.Fatalf("a relative import beside the file was missed: %+v", graph.Edges)
	}
	// node:fs is not in the repository, so it is not an edge.
	for _, edge := range graph.Edges {
		if strings.Contains(edge.To, "node:") {
			t.Fatalf("an edge left the repository: %+v", edge)
		}
	}

	bare := t.TempDir()
	write(t, filepath.Join(bare, "notes", "a.txt"), "text")
	write(t, filepath.Join(bare, "notes", "b.txt"), "more text")
	write(t, filepath.Join(bare, "top.bin"), "\x00\x01")
	third, err := Structure(t.Context(), bare)
	if err != nil {
		t.Fatal(err)
	}
	if third.Tier != TierFilesystem {
		t.Fatalf("tier = %q (%s)", third.Tier, third.TierNote)
	}
	for _, node := range third.Nodes {
		if node.Name == "notes" && node.Files != 2 {
			t.Fatalf("notes node = %+v", node)
		}
	}
}

func TestPythonImportsResolveThroughTheTable(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "app.py"), "from pkg.util import thing\nimport os\n")
	write(t, filepath.Join(root, "pkg", "util.py"), "thing = 1\n")
	graph, err := Structure(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Tier != TierImports || !strings.Contains(graph.TierNote, "Python") {
		t.Fatalf("tier = %q (%s)", graph.Tier, graph.TierNote)
	}
	if len(graph.Edges) != 1 || graph.Edges[0].From != "app.py" || graph.Edges[0].To != "pkg/util.py" {
		t.Fatalf("edges = %+v", graph.Edges)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func treeFingerprint(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	parts := []string{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, entry.Name(), info.ModTime().String())
	}
	return strings.Join(parts, "|")
}
