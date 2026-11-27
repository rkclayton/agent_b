package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/session"
)

const (
	contentSearchTimeout    = 30 * time.Second
	contentSearchFileLimit  = 8 << 20
	contentSearchByteLimit  = 64 << 20
	contentSearchEntryLimit = 20_000
)

var errContentSearchLimit = errors.New("content search limit reached")

func readSearchFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, contentSearchFileLimit+1))
}

type Grep struct {
	mu      sync.RWMutex
	cfg     config.GrepTool
	ignore  map[string]bool
	read    func(string) ([]byte, error)
	walkDir func(string, fs.WalkDirFunc) error
}

func NewGrep(cfg config.GrepTool, list config.ListDirTool) *Grep {
	ignore := make(map[string]bool, len(list.Ignore))
	for _, name := range list.Ignore {
		ignore[name] = true
	}
	return &Grep{cfg: cfg, ignore: ignore, read: readSearchFile, walkDir: filepath.WalkDir}
}
func (*Grep) Name() string { return "search_text" }
func (*Grep) Description() string {
	return "Search local file contents under path for pattern, optionally filtering filenames with glob. Unlike find_files, it returns matching text lines."
}
func (*Grep) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"pattern": map[string]any{"type": "string"}, "path": map[string]any{"type": "string", "default": "."}, "glob": map[string]any{"type": "string"}}, "required": []string{"pattern"}}
}
func (g *Grep) Call(ctx context.Context, s *session.Session, args map[string]any) (string, error) {
	cfg, ignore := g.config()
	cfg.MaxMatches = config.ContextToolLimit(cfg.MaxMatches, 50, toolWindow(s))
	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %v", err)
	}
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	glob, _ := args["glob"].(string)
	if glob != "" {
		if _, err := filepath.Match(glob, "probe"); err != nil {
			return "", fmt.Errorf("invalid glob: %v", err)
		}
	}
	readRoot, rootErr := s.ReadRoot(path)
	if rootErr != nil {
		return "", rootErr
	}
	root, err := resolveForSessionTool(ctx, s, readRoot, path)
	if err != nil {
		return "", err
	}
	search, cancel := context.WithTimeout(ctx, contentSearchTimeout)
	defer cancel()
	progress := outputObserver(ctx)
	if progress != nil {
		progress("Searching file contents")
	}
	matches := make([]string, 0, cfg.MaxMatches)
	total := 0
	skipped := 0
	covered, scanned, visited, limited, omitted := 0, 0, 0, 0, 0
	err = g.walkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if filePath != root && isInaccessible(walkErr) {
				skipped++
				return nil
			}
			return walkErr
		}
		if err := search.Err(); err != nil {
			return err
		}
		visited++
		if visited > contentSearchEntryLimit || scanned >= contentSearchByteLimit {
			return errContentSearchLimit
		}
		if entry.IsDir() {
			if filePath != root && ignore[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || credential.IsStoragePath(filePath) {
			omitted++
			return nil
		}
		if glob != "" {
			matched, _ := filepath.Match(glob, entry.Name())
			if !matched {
				return nil
			}
		}
		data, readErr := g.read(filePath)
		if readErr != nil {
			if filePath != root && isInaccessible(readErr) {
				skipped++
				return nil
			}
			return readErr
		}
		if err := search.Err(); err != nil {
			skipped++
			return err
		}
		covered++
		limit := min(contentSearchFileLimit, contentSearchByteLimit-scanned)
		if len(data) > limit {
			limited++
			data = data[:limit]
			data = data[:bytes.LastIndexByte(data, '\n')+1]
			scanned += limit
		} else {
			scanned += len(data)
		}
		if progress != nil && covered%16 == 0 {
			progress(fmt.Sprintf("Searched %d files", covered))
		}
		sample := data
		if len(sample) > 8192 {
			sample = sample[:8192]
		}
		if strings.IndexByte(string(sample), 0) >= 0 {
			return nil
		}
		rel := workspaceRel(readRoot, filePath)
		index := 0
		for line := range strings.SplitSeq(normalizeLF(string(data)), "\n") {
			index++
			if index%256 == 0 && search.Err() != nil {
				return search.Err()
			}
			if !re.MatchString(line) {
				continue
			}
			total++
			if len(matches) >= cfg.MaxMatches {
				continue
			}
			runes := []rune(line)
			if len(runes) > cfg.MaxLineChars {
				line = string(runes[:cfg.MaxLineChars]) + "…"
			}
			matches = append(matches, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), index, line))
		}
		return nil
	})
	bounded := errors.Is(err, errContentSearchLimit) || (errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil)
	if err != nil && !bounded {
		return "", err
	}
	s.TouchProject(path)
	result := "no matches"
	if total > 0 {
		result = strings.Join(matches, "\n")
	}
	if bounded || limited > 0 {
		remaining := ""
		if bounded {
			remaining = "; remaining entries were not scanned"
		}
		result += fmt.Sprintf("\n[covered %d files and %d bytes; skipped %d files and limited %d files%s]", covered, scanned, skipped+omitted, limited, remaining)
	}
	if total > len(matches) {
		result += fmt.Sprintf("\n[%d of %d matches shown]", len(matches), total)
	}
	return withSkippedInaccessible(result, skipped), nil
}
func (g *Grep) Configure(value config.Config) {
	g.mu.Lock()
	g.cfg = value.Tools.Grep
	g.ignore = make(map[string]bool, len(value.Tools.ListDir.Ignore))
	for _, name := range value.Tools.ListDir.Ignore {
		g.ignore[name] = true
	}
	g.mu.Unlock()
}
func (g *Grep) config() (config.GrepTool, map[string]bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	ignore := make(map[string]bool, len(g.ignore))
	for name, value := range g.ignore {
		ignore[name] = value
	}
	return g.cfg, ignore
}
