package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"harness/internal/config"
	"harness/internal/session"
	"harness/internal/tools"
)

const (
	delegateCopyBytes int64 = 1 << 30
	delegateCopyFiles       = 50_000
)

type helperFile struct {
	digest [32]byte
	mode   os.FileMode
	size   int64
}

type helperCopy struct {
	Root, Source string
	Before       map[string]helperFile
}

func newHelperCopy(source string, maxFiles int, maxBytes int64) (*helperCopy, error) {
	root, err := os.MkdirTemp("", "agentb-helper-")
	if err != nil {
		return nil, err
	}
	copy := &helperCopy{Root: root, Source: source, Before: map[string]helperFile{}}
	if err = copyTree(source, root, maxFiles, maxBytes, copy.Before); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("writing helper refused: %w", err)
	}
	return copy, nil
}

func (c *helperCopy) Close() { _ = os.RemoveAll(c.Root) }

func copyTree(source, target string, maxFiles int, maxBytes int64, snapshot map[string]helperFile) error {
	files, bytes := 0, int64(0)
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == "." {
			return err
		}
		destination := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("folder contains unsupported link or special file %s", filepath.ToSlash(rel))
		}
		files++
		bytes += info.Size()
		if files > maxFiles || bytes > maxBytes {
			return fmt.Errorf("folder exceeds the %d-file or %d-byte copy bound", maxFiles, maxBytes)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = in.Close()
			return err
		}
		hash := sha256.New()
		_, err = io.Copy(io.MultiWriter(out, hash), in)
		var digest [32]byte
		copy(digest[:], hash.Sum(nil))
		snapshot[filepath.Clean(rel)] = helperFile{digest: digest, mode: info.Mode(), size: info.Size()}
		closeErr, inCloseErr := out.Close(), in.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return inCloseErr
	})
}

func treeSnapshot(root string) (map[string]helperFile, error) {
	result := map[string]helperFile{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		result[filepath.Clean(rel)] = helperFile{digest: sha256.Sum256(data), mode: info.Mode(), size: int64(len(data))}
		return nil
	})
	return result, err
}

func changedFiles(before, after map[string]helperFile) []string {
	set := map[string]bool{}
	for path, old := range before {
		if current, ok := after[path]; !ok || current.digest != old.digest || current.mode.Perm() != old.mode.Perm() {
			set[path] = true
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			set[path] = true
		}
	}
	result := make([]string, 0, len(set))
	for path := range set {
		result = append(result, filepath.ToSlash(path))
	}
	sort.Strings(result)
	return result
}

func fileState(path string) (helperFile, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return helperFile{}, false, nil
	}
	if err != nil {
		return helperFile{}, false, err
	}
	info, err := os.Stat(path)
	return helperFile{digest: sha256.Sum256(data), mode: info.Mode(), size: int64(len(data))}, true, err
}

func (r *Runner) finishHelperCopy(ctx context.Context, parent *session.Session, childID string, copyRoot *helperCopy) ([]string, []string, string, error) {
	after, err := treeSnapshot(copyRoot.Root)
	if err != nil {
		return nil, nil, "", err
	}
	changed := changedFiles(copyRoot.Before, after)
	if len(changed) == 0 {
		return nil, nil, "", nil
	}
	var collided []string
	for _, rel := range changed {
		current, exists, err := fileState(filepath.Join(copyRoot.Source, filepath.FromSlash(rel)))
		baseline, had := copyRoot.Before[filepath.Clean(filepath.FromSlash(rel))]
		if err != nil || exists != had || exists && (current.digest != baseline.digest || current.mode.Perm() != baseline.mode.Perm()) {
			collided = append(collided, rel)
		}
	}
	if len(collided) > 0 {
		scratchRoot := r.cfg().Workspace
		if scratchRoot == "" {
			scratchRoot = os.TempDir()
		}
		scratch := filepath.Join(scratchRoot, parent.ID, "helper-collisions", filepath.Base(childID))
		if err := keepHelperChanges(copyRoot.Root, scratch, changed, after); err != nil {
			return nil, collided, "", err
		}
		return nil, collided, scratch, nil
	}
	if r.gate.requiredFor(parent, "write_file") {
		approved, err := r.gate.Wait(ctx, parent, parent.Snapshot().Run.RunID, r.id("delegate-apply"), "write_file", map[string]any{"paths": changed})
		if err != nil || !approved {
			return nil, nil, "", fmt.Errorf("helper changes were not applied: approval denied")
		}
	}
	changes := make([]tools.FileBatchChange, 0, len(changed))
	for _, rel := range changed {
		state, exists := after[filepath.Clean(filepath.FromSlash(rel))]
		change := tools.FileBatchChange{Path: filepath.Join(copyRoot.Source, filepath.FromSlash(rel)), Mode: state.mode, Remove: !exists}
		if exists {
			change.Data, err = os.ReadFile(filepath.Join(copyRoot.Root, filepath.FromSlash(rel)))
			if err != nil {
				return nil, nil, "", err
			}
		}
		changes = append(changes, change)
	}
	if r.delegateFiles != nil {
		err = r.delegateFiles.ApplyBatch(parent, changes)
	} else {
		for _, change := range changes {
			if change.Remove {
				err = os.Remove(change.Path)
			} else if mkdirErr := os.MkdirAll(filepath.Dir(change.Path), 0o755); mkdirErr != nil {
				err = mkdirErr
			} else {
				err = os.WriteFile(change.Path, change.Data, change.Mode.Perm())
			}
			if err != nil && !os.IsNotExist(err) {
				break
			}
		}
	}
	return changed, nil, "", err
}

func keepHelperChanges(root, scratch string, changed []string, after map[string]helperFile) error {
	for _, rel := range changed {
		target := filepath.Join(scratch, filepath.FromSlash(rel))
		if _, exists := after[filepath.Clean(filepath.FromSlash(rel))]; !exists {
			target += ".deleted"
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(target, []byte("deleted by helper\n"), 0o600); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) delegateConnection(ctx context.Context, parent *session.Session, label string) (string, string, error) {
	state := parent.Snapshot()
	if strings.TrimSpace(label) == "" {
		connection, ok := r.connection(state.ConnectionID)
		if !ok {
			return "", "", fmt.Errorf("delegate connection %s is unavailable", state.ConnectionID)
		}
		return state.ConnectionID, connection.Label, nil
	}
	var selected *config.Connection
	for i := range r.cfg().Connections {
		candidate := r.cfg().Connections[i]
		if strings.EqualFold(candidate.Label, label) || candidate.ID == label {
			selected = &candidate
			break
		}
	}
	if selected == nil {
		return "", "", fmt.Errorf("delegate connection %s is unavailable", label)
	}
	if selected.ID != state.ConnectionID {
		key := parent.ID + "\x00" + selected.ID
		r.delegateMu.Lock()
		if r.delegateApproved[key] {
			r.delegateMu.Unlock()
			return selected.ID, selected.Label, nil
		}
		wait, waiting := r.delegateApprovals[key]
		if !waiting {
			wait = make(chan struct{})
			r.delegateApprovals[key] = wait
			r.delegateMu.Unlock()
			approved, err := r.gate.WaitPolicyRequired(ctx, parent, state.Run.RunID, r.id("delegate-connection"), "delegate connection", map[string]any{"connection": selected.Label})
			r.delegateMu.Lock()
			r.delegateApproved[key] = approved && err == nil
			delete(r.delegateApprovals, key)
			close(wait)
		} else {
			r.delegateMu.Unlock()
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-wait:
			}
			r.delegateMu.Lock()
		}
		approved := r.delegateApproved[key]
		r.delegateMu.Unlock()
		if !approved {
			return "", "", fmt.Errorf("delegate connection %s was not approved", selected.Label)
		}
	}
	return selected.ID, selected.Label, nil
}

func (r *Runner) acquireDelegate(ctx context.Context, chatID, runID, connectionID string) (func(), error) {
	key := chatID + "\x00" + runID
	chatKey, connectionKey := "chat:"+chatID, "connection:"+connectionID
	r.delegateMu.Lock()
	r.delegateCalls[key]++
	if r.delegateCalls[key] > 8 {
		r.delegateMu.Unlock()
		return nil, fmt.Errorf("delegate limit of eight helpers per run reached")
	}
	for {
		limit := 1
		if connection, ok := r.cfg().Connection(connectionID); ok {
			if connection.MaxConcurrent > 0 {
				limit = connection.MaxConcurrent
			} else if connection.Capabilities.ParallelCapacity > 0 {
				limit = connection.Capabilities.ParallelCapacity
			}
		}
		if r.delegateActive[chatKey] < 4 && r.delegateActive[connectionKey] < limit {
			r.delegateActive[chatKey]++
			r.delegateActive[connectionKey]++
			r.delegateMu.Unlock()
			return func() {
				r.delegateMu.Lock()
				r.delegateActive[chatKey]--
				r.delegateActive[connectionKey]--
				close(r.delegateWake)
				r.delegateWake = make(chan struct{})
				r.delegateMu.Unlock()
			}, nil
		}
		wake := r.delegateWake
		r.delegateMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
		r.delegateMu.Lock()
	}
}
