package chatstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

func (s *Store) folder(relative string) (string, error) {
	relative = filepath.Clean(relative)
	if relative == "." { return s.root, nil }
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) { return "", fmt.Errorf("folder is outside chats") }
	return filepath.Join(s.root, relative), nil
}

func (s *Store) AddFolder(parent, name string) (string, error) {
	path, err := s.folder(parent); if err != nil { return "", err }
	name, err = AvailableName(path, name); if err != nil { return "", err }
	path = filepath.Join(path, name); return path, os.Mkdir(path, 0o700)
}

func (s *Store) RenameFolder(relative, name string) (string, error) {
	path, err := s.folder(relative); if err != nil || path == s.root { return "", errors.Join(err, fmt.Errorf("cannot rename chats root")) }
	name, err = AvailableName(filepath.Dir(path), name); if err != nil { return "", err }
	destination := filepath.Join(filepath.Dir(path), name)
	return destination, os.Rename(path, destination)
}

func (s *Store) DeleteFolder(relative string) error {
	path, err := s.folder(relative); if err != nil { return err }
	if path == s.root { return fmt.Errorf("cannot delete chats root") }
	if err := os.Remove(path); err != nil { return fmt.Errorf("folder must be empty: %w", err) }
	return nil
}

func (s *Store) Move(id, folder string) (string, error) {
	entry, found, err := s.Find(id); if err != nil || !found { return "", errors.Join(err, fmt.Errorf("chat not found")) }
	parent, err := s.folder(folder); if err != nil { return "", err }
	if info, err := os.Stat(parent); err != nil || !info.IsDir() { return "", fmt.Errorf("folder not found") }
	name, err := AvailableName(parent, filepath.Base(entry.Path)); if err != nil { return "", err }
	destination := filepath.Join(parent, name); return destination, os.Rename(entry.Path, destination)
}

func (s *Store) Rename(id, label string) (string, error) {
	entry, found, err := s.Find(id); if err != nil || !found { return "", errors.Join(err, fmt.Errorf("chat not found")) }
	name, err := AvailableName(filepath.Dir(entry.Path), label); if err != nil { return "", err }
	destination := filepath.Join(filepath.Dir(entry.Path), name)
	if err := os.Rename(entry.Path, destination); err != nil { return "", err }
	entry.Metadata.Label = label
	if err := WriteMetadata(destination, entry.Metadata); err != nil { return "", errors.Join(err, os.Rename(destination, entry.Path)) }
	return destination, nil
}
