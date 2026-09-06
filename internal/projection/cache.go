package projection

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type CacheKey struct {
	SchemaVersion int
	LogIdentity   string
	Offset        int64
}

// Cache is an optional rebuildable optimization. It never writes projection state.
type Cache struct {
	mu      sync.Mutex
	entries map[CacheKey][]byte
}

func NewCache() *Cache { return &Cache{entries: map[CacheKey][]byte{}} }

func (c *Cache) ProjectFile(path string, through int64) (Snapshot, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Snapshot{}, err
	}
	offset := through
	if offset == 0 {
		info, statErr := os.Stat(abs)
		if statErr != nil {
			return Snapshot{}, statErr
		}
		offset = info.Size()
	}
	key := CacheKey{SchemaVersion: SchemaVersion, LogIdentity: filepath.Clean(abs), Offset: offset}
	c.mu.Lock()
	if encoded, ok := c.entries[key]; ok {
		c.mu.Unlock()
		var value Snapshot
		if err := json.Unmarshal(encoded, &value); err != nil {
			return Snapshot{}, fmt.Errorf("decode cached projection %s: %w", abs, err)
		}
		return value, nil
	}
	c.mu.Unlock()
	value, _, err := ProjectFile(abs, offset)
	if err != nil {
		return Snapshot{}, fmt.Errorf("project cache %s: %w", abs, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode cached projection %s: %w", abs, err)
	}
	c.mu.Lock()
	c.entries[key] = encoded
	c.mu.Unlock()
	return value, nil
}

func (c *Cache) Clear() {
	c.mu.Lock()
	c.entries = map[CacheKey][]byte{}
	c.mu.Unlock()
}
