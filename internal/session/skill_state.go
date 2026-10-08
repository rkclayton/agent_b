package session

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const SkillStateLimit int64 = 16 << 20

var skillStateQuota = struct {
	sync.Mutex
	totals map[string]int64
}{totals: map[string]int64{}}

// ReserveSkillStateWrite enforces each skill's state bound without scanning the
// tree on every event. The first write after process start measures that skill's
// bounded store once; later writes update the cached total under one lock.
func (s *Session) ReserveSkillStateWrite(path string, size int64) (func(bool), error) {
	profileRoot := filepath.Clean(s.SkillStateRoot)
	candidate := filepath.Clean(path)
	if profileRoot == "." || !filepath.IsAbs(candidate) || !pathWithin(profileRoot, candidate) {
		return func(bool) {}, nil
	}
	relative, err := filepath.Rel(profileRoot, candidate)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	root := profileRoot
	if len(parts) > 1 {
		root = filepath.Join(profileRoot, parts[0])
	}
	skillStateQuota.Lock()
	total, found := skillStateQuota.totals[root]
	if !found {
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				info, infoErr := entry.Info()
				if infoErr != nil {
					return infoErr
				}
				total += info.Size()
			}
			return nil
		})
		if err != nil {
			skillStateQuota.Unlock()
			return nil, err
		}
		skillStateQuota.totals[root] = total
	}
	oldSize := int64(0)
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		oldSize = info.Size()
	} else if err != nil && !os.IsNotExist(err) {
		skillStateQuota.Unlock()
		return nil, err
	}
	next := total - oldSize + size
	if next > SkillStateLimit {
		skillStateQuota.Unlock()
		return nil, fmt.Errorf("skill state folder would exceed the 16 MiB limit")
	}
	var once sync.Once
	return func(committed bool) {
		once.Do(func() {
			if committed {
				skillStateQuota.totals[root] = next
			}
			skillStateQuota.Unlock()
		})
	}, nil
}
