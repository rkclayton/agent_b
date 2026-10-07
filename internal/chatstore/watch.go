package chatstore

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

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
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() { <-timer.C }
	defer timer.Stop()
	pending := false
	pendingReason := ""
	schedule := func(reason string) {
		pendingReason = reason
		if pending && !timer.Stop() { select { case <-timer.C: default: } }
		pending = true
		timer.Reset(250 * time.Millisecond)
	}
	for {
		select {
		case <-ctx.Done(): return nil
		case event, ok := <-w.Events:
			if !ok { return nil }
			schedule(event.Op.String() + " " + event.Name)
		case watchErr, ok := <-w.Errors:
			if !ok { return nil }
			if errors.Is(watchErr, fsnotify.ErrEventOverflow) {
				schedule("watcher overflow")
			} else { log.Printf("chat tree watcher: %v", watchErr) }
		case <-timer.C:
			pending = false
			log.Printf("chat tree changed: %s; full rescan", pendingReason)
			if err := rescan(); err != nil { log.Printf("chat tree rescan: %v", err) }
		}
	}
}
