package chatstore

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// Scan reads the directory tree itself; there is deliberately no second index.
func (s *Store) Scan() ([]Entry, error) {
	var entries []Entry
	err := filepath.WalkDir(s.root, func(path string, item fs.DirEntry, err error) error {
		if err != nil { return err }
		if !item.IsDir() || path == s.root { return nil }
		metadata, err := ReadMetadata(path)
		if err == nil {
			entries = append(entries, Entry{Path: path, Metadata: metadata})
			return filepath.SkipDir
		}
		return nil
	})
	if os.IsNotExist(err) { return entries, nil }
	return entries, err
}

// Watch reports an initial scan and every later on-disk change. An overflow is
// not a partial result: it is logged and followed by the same full rescan.
func (s *Store) Watch(ctx context.Context, changed func([]Entry)) error {
	if err := os.MkdirAll(s.root, 0o700); err != nil { return err }
	w, err := fsnotify.NewWatcher()
	if err != nil { return err }
	defer w.Close()
	rescan := func() error {
		entries, err := s.Scan()
		if err != nil { return err }
		wanted := map[string]bool{}
		_ = filepath.WalkDir(s.root, func(path string, item fs.DirEntry, walkErr error) error {
			if walkErr == nil && item.IsDir() { wanted[path] = true }
			return nil
		})
		for _, path := range w.WatchList() { if !wanted[path] { _ = w.Remove(path) } else { delete(wanted, path) } }
		for path := range wanted { if err := w.AddWith(path, fsnotify.WithBufferSize(64 * 1024)); err != nil && !os.IsNotExist(err) { return err } }
		changed(entries)
		return nil
	}
	if err := rescan(); err != nil { return err }
	for {
		select {
		case <-ctx.Done(): return nil
		case event, ok := <-w.Events:
			if !ok { return nil }
			log.Printf("chat tree changed: %s %s; full rescan", event.Op, event.Name)
			if err := rescan(); err != nil { log.Printf("chat tree rescan: %v", err) }
		case watchErr, ok := <-w.Errors:
			if !ok { return nil }
			if errors.Is(watchErr, fsnotify.ErrEventOverflow) {
				log.Printf("chat tree watcher overflow; full rescan")
				if err := rescan(); err != nil { log.Printf("chat tree rescan: %v", err) }
			} else { log.Printf("chat tree watcher: %v", watchErr) }
		}
	}
}
