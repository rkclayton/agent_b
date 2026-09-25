package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"harness/internal/config"
	"harness/internal/session"
)

// FileIdentity applies the same configured Windows identity to the built-in
// file tools that Shell uses. When enabled, absolute paths are authorized by
// that identity's OS token instead of by the harness operator's token.
type FileIdentity struct {
	mu              sync.RWMutex
	service         config.ShellServiceAccount
	operatorContext bool
	unavailable     string
	credential      shellCredentialReader
	run             serviceFileRunner
}

type serviceFileRunner func(config.ShellServiceAccount, []byte, func() (string, error)) (string, error)

type serviceFileIdentityError struct{ err error }

func (e *serviceFileIdentityError) Error() string { return e.err.Error() }
func (e *serviceFileIdentityError) Unwrap() error { return e.err }

func NewFileIdentity(credential shellCredentialReader) *FileIdentity {
	return &FileIdentity{credential: credential, run: runAsServiceFileIdentity}
}

func (p *FileIdentity) Configure(cfg config.Config) {
	p.mu.Lock()
	p.service = cfg.Shell.ServiceAccount
	p.operatorContext = cfg.Shell.OperatorContext
	p.unavailable = ""
	p.mu.Unlock()
}

func (p *FileIdentity) Wrap(tool Tool) Tool {
	return &identityFileTool{tool: tool, identity: p}
}

func (p *FileIdentity) snapshot() (config.ShellServiceAccount, bool, shellCredentialReader, serviceFileRunner) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.service, p.operatorContext, p.credential, p.run
}

type identityFileTool struct {
	tool     Tool
	identity *FileIdentity
}

func (t *identityFileTool) Name() string           { return t.tool.Name() }
func (t *identityFileTool) Schema() map[string]any { return t.tool.Schema() }

func (t *identityFileTool) Description() string {
	return t.tool.Description()
}

func (t *identityFileTool) Configure(cfg config.Config) {
	if configurableTool, ok := t.tool.(configurable); ok {
		configurableTool.Configure(cfg)
	}
	t.identity.Configure(cfg)
}

func (t *identityFileTool) Call(ctx context.Context, s *session.Session, args map[string]any) (string, error) {
	detail := t.CallDetailed(ctx, s, args)
	return detail.Content, detail.Err
}

func (t *identityFileTool) CallDetailed(ctx context.Context, s *session.Session, args map[string]any) CallDetail {
	service, operatorContext, credential, runner := t.identity.snapshot()
	t.identity.mu.RLock()
	unavailable := t.identity.unavailable
	t.identity.mu.RUnlock()
	if operatorContext {
		result, err := t.tool.Call(withOSPathPolicy(ctx), s, args)
		return CallDetail{Content: result, Err: err, OperatorContext: true}
	}
	if !service.Enabled || unavailable != "" {
		result, err := t.tool.Call(ctx, s, args)
		// Item 2fi: with no service identity the outside-folder refusal offers
		// the same operator decision the service posture offers, and holds;
		// declining leaves the refusal as the tool's result.
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "path is outside the folder") {
			// Item 2fy: a card widens the folder for a file that exists; for one
			// that does not, the tool says so and no card is raised. The harness's
			// own view decides here, since there is no service account.
			if target := outsideTarget(s, t.tool.Name(), args); targetMissing(target) {
				return missingOutside(t.tool.Name(), target)
			} else if processCanRead(target) && t.tool.Name() != "write_file" && t.tool.Name() != "edit_file" {
				return CallDetail{Err: err}
			}
			return CallDetail{Content: "file operation was not completed: path is outside the folder", OperatorOverrideReason: "path is outside the folder"}
		}
		return CallDetail{Content: result, Err: err}
	}
	if credential == nil {
		return fileIdentityOverride("service-account credential is not configured")
	}
	password, err := credential.Read()
	if err != nil {
		return fileIdentityOverride(err.Error())
	}
	defer clearBytes(password)
	// Item 2fy: existence is judged in the service account's own view, on the
	// impersonated thread; only a definite not-found skips the card (a denied
	// stat means the path exists — v0.71.0/W1 measured the two apart).
	missing := false
	result, err := runner(service, password, func() (string, error) {
		result, err := t.tool.Call(ctx, s, args)
		// v0.71.0 cold review: only the jail's outside-folder refusal is judged;
		// a permission error inside the folder is the service account's denial
		// and keeps its card, whether or not the target exists yet.
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "path is outside the folder") {
			missing = targetMissing(outsideTarget(s, t.tool.Name(), args))
		}
		return result, err
	})
	if err == nil {
		return CallDetail{Content: result}
	}
	if missing {
		return missingOutside(t.tool.Name(), outsideTarget(s, t.tool.Name(), args))
	}
	var identityErr *serviceFileIdentityError
	if errors.As(err, &identityErr) {
		return fileIdentityOverride(identityErr.Error())
	}
	if errors.Is(err, os.ErrPermission) {
		return fileIdentityOverride("service account was denied permission for the requested path")
	}
	if strings.Contains(strings.ToLower(err.Error()), "path is outside the folder") {
		return fileIdentityOverride("bound-directory jail: path is outside the folder")
	}
	return CallDetail{Err: err}
}

func processCanRead(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false
	}
	if info.IsDir() {
		_, err = file.Readdirnames(1)
		return err == nil || errors.Is(err, io.EOF)
	}
	var one [1]byte
	_, err = file.Read(one[:])
	return err == nil || errors.Is(err, io.EOF)
}

func (t *identityFileTool) PreflightServiceIdentity() error {
	service, operatorContext, credential, runner := t.identity.snapshot()
	if !service.Enabled || operatorContext {
		return nil
	}
	if credential == nil {
		return t.identity.markUnavailable("service-account credential is not configured")
	}
	password, err := credential.Read()
	if err == nil {
		_, err = runner(service, password, func() (string, error) { return "", nil })
	}
	clearBytes(password)
	if err != nil {
		return t.identity.markUnavailable(err.Error())
	}
	return nil
}

func (p *FileIdentity) markUnavailable(reason string) error {
	p.mu.Lock()
	p.unavailable = reason
	p.mu.Unlock()
	return fmt.Errorf("%s", reason)
}

// CallAsOperator is dispatcher-only. It deliberately bypasses both the
// service identity and workspace path boundary for one explicitly approved
// replay of the exact tool call.
func (t *identityFileTool) CallAsOperator(ctx context.Context, s *session.Session, args map[string]any) (string, error) {
	return t.tool.Call(withOSPathPolicy(ctx), s, args)
}

// outsideTarget is the call's own path resolved as the tool resolves it: a
// relative path is taken from the tool's read or write root (a d chat's is its
// plan folder), falling back to the chat's folder.
func outsideTarget(s *session.Session, name string, args map[string]any) string {
	path, _ := args["path"].(string)
	if strings.TrimSpace(path) == "" {
		return ""
	}
	path = filepath.FromSlash(path)
	if !filepath.IsAbs(path) && s != nil {
		rootFor := s.ReadRoot
		if name == "write_file" || name == "edit_file" {
			rootFor = s.WriteRoot
		}
		root, err := rootFor(path)
		if err != nil || root == "" {
			root = s.Workspace
		}
		path = filepath.Join(root, path)
	}
	return filepath.Clean(path)
}

// targetMissing is a definite not-found for the path, judged by the identity
// the caller runs as; anything else — present, denied, unknown — is not.
func targetMissing(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Lstat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// missingOutside answers a call on a path outside the folder that does not
// exist: a plain tool error naming the boundary, never a card. A write to a
// new file outside the folder is refused outright, by item 2fy's contract ("a
// write can never be widened by a card"); before it, the card's operator
// replay could create one.
func missingOutside(name, path string) CallDetail {
	if name == "write_file" || name == "edit_file" {
		return CallDetail{Err: fmt.Errorf("path is outside the folder and does not exist: %s; a write outside the folder is refused and there is nothing for the operator to allow — write inside your folder", path)}
	}
	return CallDetail{Err: fmt.Errorf("no such file or directory: %s; it is outside the folder, so there is nothing for the operator to allow — check the path, or use a file inside your folder", path)}
}

func fileIdentityOverride(reason string) CallDetail {
	return CallDetail{
		Content:                fmt.Sprintf("service-account file operation was not completed: %s", reason),
		OperatorOverrideReason: reason,
	}
}
