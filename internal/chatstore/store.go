package chatstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const MetadataFile = "chat.json"

type Metadata struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Label   string    `json:"label"`
}

type Entry struct {
	Path     string   `json:"path"`
	Metadata Metadata `json:"chat"`
}

type Store struct{ root string }

func New(root string) *Store  { return &Store{root: filepath.Clean(root)} }
func (s *Store) Root() string { return s.root }
func (s *Store) Create(id, label string, created time.Time) (string, error) {
	if id == "" {
		return "", fmt.Errorf("chat id is empty")
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return "", err
	}
	for {
		name, err := AvailableName(s.root, label)
		if err != nil {
			return "", err
		}
		path := filepath.Join(s.root, name)
		if err := os.Mkdir(path, 0o700); os.IsExist(err) {
			continue
		} else if err != nil {
			return "", err
		}
		if err := WriteMetadata(path, Metadata{ID: id, Created: created.UTC(), Label: label}); err != nil {
			_ = os.Remove(path)
			return "", err
		}
		return path, nil
	}
}

func WriteMetadata(path string, metadata Metadata) error {
	content, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	temporary := filepath.Join(path, ".chat.json.tmp")
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, filepath.Join(path, MetadataFile)); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func ReadMetadata(path string) (Metadata, error) {
	content, err := os.ReadFile(filepath.Join(path, MetadataFile))
	if err != nil {
		return Metadata{}, err
	}
	var metadata Metadata
	if err := json.Unmarshal(content, &metadata); err != nil {
		return Metadata{}, err
	}
	if metadata.ID == "" || metadata.Created.IsZero() {
		return Metadata{}, fmt.Errorf("chat.json is missing id or created")
	}
	return metadata, nil
}

func (s *Store) Find(id string) (Entry, bool, error) {
	var found Entry
	err := filepath.WalkDir(s.root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !item.IsDir() || path == s.root {
			return nil
		}
		metadata, err := ReadMetadata(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return nil
		}
		if metadata.ID != id {
			return filepath.SkipDir
		}
		if found.Path != "" {
			return fmt.Errorf("duplicate chat id %q", id)
		}
		found = Entry{Path: path, Metadata: metadata}
		return filepath.SkipDir
	})
	if os.IsNotExist(err) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return found, found.Path != "", nil
}

// Migrate moves one legacy scratch directory into the visible tree. The old
// path is the marker: once it is absent and chat.json is found, the move is done.
func (s *Store) Migrate(legacy, id, label string, created time.Time) (string, bool, error) {
	if entry, found, err := s.Find(id); err != nil {
		return "", false, err
	} else if found {
		if _, legacyErr := os.Lstat(legacy); legacyErr == nil {
			return "", false, fmt.Errorf("chat %s exists at both %s and %s", id, legacy, entry.Path)
		} else if !os.IsNotExist(legacyErr) {
			return "", false, legacyErr
		}
		return entry.Path, false, nil
	}
	info, err := os.Lstat(legacy)
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("legacy chat path is not a directory: %s", legacy)
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return "", false, err
	}
	for {
		name, err := AvailableName(s.root, label)
		if err != nil {
			return "", false, err
		}
		destination := filepath.Join(s.root, name)
		if err := os.Rename(legacy, destination); os.IsExist(err) {
			continue
		} else if err != nil {
			return "", false, err
		}
		if err := WriteMetadata(destination, Metadata{ID: id, Created: created.UTC(), Label: label}); err != nil {
			return "", false, errors.Join(err, os.Rename(destination, legacy))
		}
		return destination, true, nil
	}
}
