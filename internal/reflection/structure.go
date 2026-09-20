package reflection

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Structure extraction (item 17-i, step 2). The shape of a repository comes
// from the code, never from a model and never from run history. Three tiers,
// degrading silently, each reporting which one produced the graph.
//
// HARD CONSTRAINT: nothing here modifies the project or fetches anything. The
// tier-1 resolvers are run with their offline flags and with the module proxy
// off; a resolver that would need the network is skipped and the repository
// falls to tier 2.

// Tier names, as reported beside the graph.
const (
	TierResolver   = "tier 1"
	TierImports    = "tier 2"
	TierFilesystem = "tier 3"
)

// Node is one unit of the graph: a package, a module or a file.
type Node struct {
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`
	Files int    `json:"files,omitempty"`
	Bytes int64  `json:"bytes,omitempty"`
}

// Edge is a dependency from one node to another, both inside the repository.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Graph is the extracted structure with the tier that produced it.
type Graph struct {
	Root     string `json:"root"`
	Tier     string `json:"tier"`
	TierNote string `json:"tier_note"`
	Nodes    []Node `json:"nodes"`
	Edges    []Edge `json:"edges"`
}

// resolver is one tier-1 toolchain: the file that marks the project, the
// command to run, and how to read its answer. Only resolvers that ship with
// the toolchain are listed — the project is already built with them, so there
// is no install burden — and each runs offline.
type resolver struct {
	Language string
	Marker   string
	Command  []string
	Env      []string
	Parse    func(root string, output []byte) (nodes []Node, edges []Edge, err error)
}

func resolvers() []resolver {
	return []resolver{
		{Language: "Go", Marker: "go.mod", Command: []string{"go", "list", "-deps", "-json", "./..."}, Env: []string{"GOPROXY=off", "GOFLAGS=-mod=readonly", "GOWORK=off"}, Parse: parseGoList},
		{Language: "Rust", Marker: "Cargo.toml", Command: []string{"cargo", "metadata", "--offline", "--format-version", "1", "--no-deps"}, Parse: parseCargoMetadata},
	}
}

// goPackage is the part of `go list -json` this reads.
type goPackage struct {
	ImportPath string                 `json:"ImportPath"`
	Dir        string                 `json:"Dir"`
	Module     *struct{ Path string } `json:"Module"`
	GoFiles    []string               `json:"GoFiles"`
	Imports    []string               `json:"Imports"`
	Standard   bool                   `json:"Standard"`
}

// parseGoList keeps the repository's own packages and the edges between them.
// The compiler's answer is the complete answer: Go has no conditional imports.
func parseGoList(root string, output []byte) ([]Node, []Edge, error) {
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	own := map[string]goPackage{}
	order := []string{}
	for {
		var pkg goPackage
		if err := decoder.Decode(&pkg); err != nil {
			break
		}
		if pkg.Standard || pkg.Dir == "" {
			continue
		}
		if inside, err := within(root, pkg.Dir); err != nil || !inside {
			continue
		}
		own[pkg.ImportPath] = pkg
		order = append(order, pkg.ImportPath)
	}
	if len(own) == 0 {
		return nil, nil, fmt.Errorf("no package of this repository in the resolver output")
	}
	sort.Strings(order)
	nodes := make([]Node, 0, len(order))
	edges := []Edge{}
	for _, importPath := range order {
		pkg := own[importPath]
		relative, _ := filepath.Rel(root, pkg.Dir)
		var bytes int64
		for _, name := range pkg.GoFiles {
			if info, err := os.Stat(filepath.Join(pkg.Dir, name)); err == nil {
				bytes += info.Size()
			}
		}
		nodes = append(nodes, Node{Name: importPath, Path: filepath.ToSlash(relative), Files: len(pkg.GoFiles), Bytes: bytes})
		for _, imported := range pkg.Imports {
			if _, ok := own[imported]; ok {
				edges = append(edges, Edge{From: importPath, To: imported})
			}
		}
	}
	return nodes, edges, nil
}

// parseCargoMetadata reads the workspace's own packages.
func parseCargoMetadata(root string, output []byte) ([]Node, []Edge, error) {
	var metadata struct {
		Packages []struct {
			Name         string `json:"name"`
			ManifestPath string `json:"manifest_path"`
			Dependencies []struct {
				Name string `json:"name"`
			} `json:"dependencies"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(output, &metadata); err != nil {
		return nil, nil, err
	}
	own := map[string]bool{}
	for _, pkg := range metadata.Packages {
		own[pkg.Name] = true
	}
	nodes, edges := []Node{}, []Edge{}
	for _, pkg := range metadata.Packages {
		relative, _ := filepath.Rel(root, filepath.Dir(pkg.ManifestPath))
		nodes = append(nodes, Node{Name: pkg.Name, Path: filepath.ToSlash(relative)})
		for _, dependency := range pkg.Dependencies {
			if own[dependency.Name] {
				edges = append(edges, Edge{From: pkg.Name, To: dependency.Name})
			}
		}
	}
	if len(nodes) == 0 {
		return nil, nil, fmt.Errorf("no package in the resolver output")
	}
	return nodes, edges, nil
}

// importPattern is tier 2: one extractor driven by a table. Adding a language
// is a row here, not a module. An unmatched form is a missing edge, never an
// error.
type importPattern struct {
	Language   string
	Extensions []string
	Pattern    *regexp.Regexp
	// Relative says whether a matched target is a path relative to the
	// importing file (JS) or a dotted module name (Python).
	Relative bool
}

func importPatterns() []importPattern {
	return []importPattern{
		{Language: "JavaScript/TypeScript", Extensions: []string{".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx"},
			Pattern: regexp.MustCompile(`(?m)^\s*(?:import\s[^'"\n]*from\s*|import\s*|export\s[^'"\n]*from\s*)['"]([^'"]+)['"]|require\(\s*['"]([^'"]+)['"]\s*\)`), Relative: true},
		{Language: "Python", Extensions: []string{".py"},
			Pattern: regexp.MustCompile(`(?m)^\s*(?:from\s+([\w.]+)\s+import|import\s+([\w.]+))`)},
	}
}

// maxScannedFiles bounds tier 2 and tier 3: reflection reads a repository, it
// does not crawl a disk.
const maxScannedFiles = 4000

func skipDirectory(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "build", "target", "__pycache__", ".venv", "venv", ".idea", ".vscode", "bin", "obj":
		return true
	}
	return false
}

// walkFiles lists the repository's files, bounded, skipping the directories
// nobody means by "the code".
func walkFiles(root string) ([]string, bool, error) {
	files := []string{}
	truncated := false
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if current != root && skipDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= maxScannedFiles {
			truncated = true
			return filepath.SkipAll
		}
		files = append(files, current)
		return nil
	})
	return files, truncated, err
}

// tierTwo extracts imports with the pattern table.
func tierTwo(root string) (*Graph, error) {
	files, truncated, err := walkFiles(root)
	if err != nil {
		return nil, err
	}
	patterns := importPatterns()
	byExtension := map[string]importPattern{}
	for _, pattern := range patterns {
		for _, extension := range pattern.Extensions {
			byExtension[extension] = pattern
		}
	}
	known := map[string]bool{}
	for _, file := range files {
		relative, _ := filepath.Rel(root, file)
		known[filepath.ToSlash(relative)] = true
	}
	nodes, edges := []Node{}, []Edge{}
	languages := map[string]int{}
	for _, file := range files {
		pattern, ok := byExtension[strings.ToLower(filepath.Ext(file))]
		if !ok {
			continue
		}
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		relative := filepath.ToSlash(mustRel(root, file))
		nodes = append(nodes, Node{Name: relative, Path: relative, Files: 1, Bytes: info.Size()})
		languages[pattern.Language]++
		source, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, match := range pattern.Pattern.FindAllStringSubmatch(string(source), -1) {
			target := ""
			for _, group := range match[1:] {
				if group != "" {
					target = group
					break
				}
			}
			if target == "" {
				continue
			}
			if resolved := resolveImport(relative, target, pattern.Relative, known); resolved != "" {
				edges = append(edges, Edge{From: relative, To: resolved})
			}
		}
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no file matched the import table")
	}
	names := make([]string, 0, len(languages))
	for language := range languages {
		names = append(names, language)
	}
	sort.Strings(names)
	note := "generic import extractor: " + strings.Join(names, ", ")
	if truncated {
		note += fmt.Sprintf("; stopped at %d files", maxScannedFiles)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return &Graph{Root: root, Tier: TierImports, TierNote: note, Nodes: nodes, Edges: edges}, nil
}

// resolveImport turns an import target into a file in the repository, or "".
// A target that resolves outside the repository is not an edge.
func resolveImport(from, target string, relative bool, known map[string]bool) string {
	if relative {
		if !strings.HasPrefix(target, ".") {
			return "" // a package, not a file in this repository
		}
		candidate := path.Clean(path.Join(path.Dir(from), target))
		for _, suffix := range []string{"", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", "/index.js", "/index.mjs", "/index.ts"} {
			if known[candidate+suffix] {
				return candidate + suffix
			}
		}
		return ""
	}
	dotted := strings.TrimLeft(target, ".")
	base := strings.ReplaceAll(dotted, ".", "/")
	for _, suffix := range []string{".py", "/__init__.py"} {
		if known[base+suffix] {
			return base + suffix
		}
		if directory := path.Dir(from); directory != "." {
			if known[path.Join(directory, base)+suffix] {
				return path.Join(directory, base) + suffix
			}
		}
	}
	return ""
}

// tierThree is the filesystem structure: the directory tree with sizes. No
// parsing, universal, weaker but useful.
func tierThree(root string) (*Graph, error) {
	files, truncated, err := walkFiles(root)
	if err != nil {
		return nil, err
	}
	byDirectory := map[string]*Node{}
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		directory := filepath.ToSlash(filepath.Dir(mustRel(root, file)))
		node, ok := byDirectory[directory]
		if !ok {
			node = &Node{Name: directory, Path: directory}
			byDirectory[directory] = node
		}
		node.Files++
		node.Bytes += info.Size()
	}
	nodes, edges := []Node{}, []Edge{}
	for _, node := range byDirectory {
		nodes = append(nodes, *node)
		if parent := path.Dir(node.Name); parent != node.Name && byDirectory[parent] != nil {
			edges = append(edges, Edge{From: parent, To: node.Name})
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	note := "directory tree and sizes"
	if truncated {
		note += fmt.Sprintf("; stopped at %d files", maxScannedFiles)
	}
	return &Graph{Root: root, Tier: TierFilesystem, TierNote: note, Nodes: nodes, Edges: edges}, nil
}

func mustRel(root, file string) string {
	relative, err := filepath.Rel(root, file)
	if err != nil {
		return file
	}
	return relative
}

func within(root, dir string) (bool, error) {
	relative, err := filepath.Rel(root, dir)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

// Structure extracts a repository's shape, trying each tier in turn. It never
// writes to the repository and never reaches the network.
func Structure(ctx context.Context, root string) (*Graph, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("structure: %s is not a directory", root)
	}
	for _, current := range resolvers() {
		if _, err := os.Stat(filepath.Join(root, current.Marker)); err != nil {
			continue
		}
		executable, err := exec.LookPath(current.Command[0])
		if err != nil {
			continue // the toolchain is not on PATH: fall to tier 2
		}
		timed, cancel := context.WithTimeout(ctx, 3*time.Minute)
		command := exec.CommandContext(timed, executable, current.Command[1:]...)
		command.Dir = root
		command.Env = append(os.Environ(), current.Env...)
		output, runErr := command.Output()
		cancel()
		if runErr != nil && len(output) == 0 {
			continue // offline or unhappy: fall to tier 2 rather than reach the network
		}
		nodes, edges, parseErr := current.Parse(root, output)
		if parseErr != nil {
			continue
		}
		return &Graph{Root: root, Tier: TierResolver, TierNote: current.Language + ": " + strings.Join(current.Command, " "), Nodes: nodes, Edges: edges}, nil
	}
	if graph, err := tierTwo(root); err == nil {
		return graph, nil
	}
	return tierThree(root)
}

// Text renders the graph as the text tree that lands beside the plan. Bounded:
// the biggest nodes, with the rest counted.
func (g *Graph) Text(limit int) string {
	lines := []string{fmt.Sprintf("Structure of %s — %s (%s)", filepath.Base(g.Root), g.Tier, g.TierNote),
		fmt.Sprintf("%d nodes, %d edges inside the repository", len(g.Nodes), len(g.Edges))}
	ranked := append([]Node{}, g.Nodes...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Bytes != ranked[j].Bytes {
			return ranked[i].Bytes > ranked[j].Bytes
		}
		return ranked[i].Name < ranked[j].Name
	})
	outgoing := map[string]int{}
	for _, edge := range g.Edges {
		outgoing[edge.From]++
	}
	shown := ranked
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	for _, node := range shown {
		line := "  " + node.Name
		if node.Files > 0 {
			line += fmt.Sprintf(" · %d files", node.Files)
		}
		if node.Bytes > 0 {
			line += fmt.Sprintf(" · %d KiB", node.Bytes/1024)
		}
		if outgoing[node.Name] > 0 {
			line += fmt.Sprintf(" · %d deps", outgoing[node.Name])
		}
		lines = append(lines, line)
	}
	if rest := len(ranked) - len(shown); rest > 0 {
		lines = append(lines, fmt.Sprintf("  … and %d more", rest))
	}
	return strings.Join(lines, "\n")
}

// scanLines is a small helper for tests and callers that read a graph's text.
func scanLines(text string) []string {
	scanner := bufio.NewScanner(strings.NewReader(text))
	lines := []string{}
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}
