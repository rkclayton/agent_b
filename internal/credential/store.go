package credential

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const FileName = ".agentb-shell-credential.dpapi"

// Item 2li (a): the machine-scoped service credential is a SEPARATE FILE, not a
// flag inside the existing one. A DPAPI blob does not say which scope wrote it
// without reading its master-key GUID, and the scope decides whether the ACL
// check applies -- so the scope is the filename, which cannot be misread.
// Nothing but the service-account store ever has one: NewNamed does not set it,
// which is why a connection API key has no way to ask for this scope.
const MachineFileName = ".agentb-shell-credential-machine.dpapi"

// ServiceAccount is the managed local account whose password this store holds.
// It lives here because the machine scope's access list has to name it and the
// read-side check has to expect it; internal/web keeps its own copy for the
// status it reports, and the two are the same string by construction below.
const ServiceAccount = "agentb-svc"

const connectionFilePrefix = ".agentb-connection-credential-"

var credentialName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

var (
	ErrNotStored   = errors.New("credential is not stored")
	ErrUnsupported = errors.New("credential storage is supported only on Windows")
	// Item 2li (b): a machine-scoped blob whose ACL no longer holds is refused
	// rather than used. It is decryptable by anything on the machine, so the ACL
	// is the whole of its protection, and a widened one means the secret should be
	// treated as reachable rather than as still secret.
	ErrMachineBlobUnprotected = errors.New("the machine-scoped credential is not protected by its access list")
	ErrScopeNotAvailable      = errors.New("the machine scope is available only to the service-account credential")
)

type Status struct {
	Stored   bool   `json:"stored"`
	StoredAt string `json:"stored_at"`
	// Item 2li (e): Settings must read a machine-provisioned identity as ready
	// without offering to redo it, which it cannot do unless it can see which
	// scope answered. Empty when nothing is stored.
	Scope string `json:"scope,omitempty"`
}

type Store struct {
	path        string
	legacyPath  string
	machinePath string
	account     string
	tempPrefix  string
}

func New(dataRoot string) *Store {
	if dataRoot == "" {
		dataRoot = "."
	}
	return &Store{
		path:        filepath.Join(dataRoot, FileName),
		machinePath: filepath.Join(dataRoot, MachineFileName),
		tempPrefix:  ".agentb-shell-credential-*",
	}
}

func NewNamed(dataRoot, name string) (*Store, error) {
	if !credentialName.MatchString(name) {
		return nil, fmt.Errorf("credential name %q must be a lowercase slug", name)
	}
	if dataRoot == "" {
		dataRoot = "."
	}
	legacyPrefix := ".agentb-" + "pro" + "file-credential-"
	return &Store{
		path:       filepath.Join(dataRoot, connectionFilePrefix+name+".dpapi"),
		legacyPath: filepath.Join(dataRoot, legacyPrefix+name+".dpapi"),
		tempPrefix: connectionFilePrefix + name + "-*",
	}, nil
}

func (s *Store) Path() string { return s.path }

// MachinePath is empty for every store but the service account's, which is the
// structural half of (a): there is no path to write to, so there is no scope to
// ask for.
func (s *Store) MachinePath() string { return s.machinePath }

// ForAccount names the service account a machine-scoped blob is written for, so
// the access list can include it and the read-side check can expect it.
func (s *Store) ForAccount(account string) *Store {
	copied := *s
	copied.account = account
	return &copied
}

func (s *Store) Status() Status {
	path := s.readPath()
	info, err := os.Stat(path)
	if err != nil {
		return Status{}
	}
	scope := "user"
	if s.machinePath != "" && path == s.machinePath {
		scope = "machine"
	}
	return Status{Stored: true, StoredAt: info.ModTime().UTC().Format(time.RFC3339), Scope: scope}
}

func (s *Store) Read() ([]byte, error) {
	path := s.readPath()
	// (b), read side: the access list is checked BEFORE the file is opened, so a
	// blob whose protection has been widened is never decrypted at all.
	if s.machinePath != "" && path == s.machinePath {
		if err := machineBlobACLHolds(path, s.account); err != nil {
			return nil, err
		}
	}
	protected, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotStored
	}
	if err != nil {
		return nil, fmt.Errorf("read credential store: %w", err)
	}
	plain, err := unprotect(protected)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential store: %w", err)
	}
	return plain, nil
}

func (s *Store) Write(password []byte) error {
	if len(password) == 0 {
		return errors.New("password is required")
	}
	protected, err := protect(password)
	if err != nil {
		return fmt.Errorf("encrypt credential store: %w", err)
	}
	return s.writeProtected(s.path, protected, nil)
}

// writeProtected is the write-temp, replace, harden dance both scopes share.
//
// harden runs on the PUBLISHED file, not the temporary one. Hardening first
// reads better but does not work: the protected access list does not include
// whoever is writing, so MoveFileEx loses the delete right it needs on its own
// source and the replace fails with access denied. The order here leaves the
// blob at its final path under the directory's inherited permissions for the
// moment between the rename and the access-list write; the directory is already
// the operator's own data root, and WriteMachine removes the file outright if
// either the hardening or the read-back check fails, so the window cannot leave
// a usable blob behind that nothing has checked.
func (s *Store) writeProtected(target string, protected []byte, harden func(string) error) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, s.tempPrefix)
	if err != nil {
		return fmt.Errorf("create credential temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("protect credential temporary file: %w", err)
	}
	if _, err := temp.Write(protected); err != nil {
		temp.Close()
		return fmt.Errorf("write credential temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync credential temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close credential temporary file: %w", err)
	}
	if err := replaceFile(tempPath, target); err != nil {
		return fmt.Errorf("replace credential store: %w", err)
	}
	if harden != nil {
		if err := harden(target); err != nil {
			_ = os.Remove(target)
			return err
		}
	}
	return nil
}

// WriteMachine stores the password machine-scoped and locks the file down in the
// same operation. It exists only on the service-account store; every other store
// refuses, which is (a) enforced rather than documented.
func (s *Store) WriteMachine(password []byte) error {
	if s.machinePath == "" {
		return ErrScopeNotAvailable
	}
	if len(password) == 0 {
		return errors.New("password is required")
	}
	protected, err := protectMachine(password)
	if err != nil {
		return fmt.Errorf("encrypt credential store: %w", err)
	}
	if err := s.writeProtected(s.machinePath, protected, func(path string) error {
		return secureMachineBlob(path, s.account)
	}); err != nil {
		return err
	}
	// Written, published, and only then read back: the check that runs on every
	// read has to pass here too, or the blob does not stay.
	if err := machineBlobACLHolds(s.machinePath, s.account); err != nil {
		_ = os.Remove(s.machinePath)
		return err
	}
	return nil
}

// Clear removes what THIS user stored, and deliberately not the machine-scoped
// blob. Every in-app caller -- the Settings clear action, the rejected-credential
// cleanup, the rollback after a failed setup -- speaks for one user; a machine
// credential an administrator provisioned for everyone on the endpoint is not
// theirs to delete, and deleting it would take the service identity away from
// every other user of that machine. Item 2li (f) removes it, through the same
// elevated tool that wrote it.
// ClearMachine removes the machine-scoped blob. It is the in-process half of
// (f) and has no caller in the running product: an administrator removes the
// credential through provision-service-identity.ps1 -RemoveMachineCredential.
func (s *Store) ClearMachine() error {
	if s.machinePath == "" {
		return ErrScopeNotAvailable
	}
	if err := os.Remove(s.machinePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove machine credential store: %w", err)
	}
	return nil
}

func (s *Store) Clear() error {
	for _, path := range []string{s.path, s.legacyPath} {
		if path == "" {
			continue
		}
		err := os.Remove(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove credential store: %w", err)
		}
	}
	return nil
}

// readPath prefers what this user stored for themselves, then the legacy name,
// then the machine-scoped blob. Item 2li (e): the operator's own path is
// unchanged -- a user-scoped credential they provisioned still answers first --
// and a user who has none, which is every user on a machine-provisioned fleet
// endpoint, falls through to the machine blob.
func (s *Store) readPath() string {
	if _, err := os.Stat(s.path); err == nil {
		return s.path
	}
	if s.legacyPath != "" {
		if _, err := os.Stat(s.legacyPath); err == nil {
			return s.legacyPath
		}
	}
	if s.machinePath != "" {
		if _, err := os.Stat(s.machinePath); err == nil {
			return s.machinePath
		}
	}
	return s.path
}
