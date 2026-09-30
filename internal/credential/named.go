package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Item 2nv (a): STORAGE, and only storage. A vault credential is kept exactly the way
// this package already keeps a model connection's key — one DPAPI blob per name under the
// operator data root, user scope, written through the same Store — and the vault adds
// only the plain record of what each one is for: its origin, the header it is sent as,
// and when it was stored. The record never holds a value; the value never leaves this
// package except through Secret(), which the auth provider calls and nothing else.
//
// (j) is the honest part: DPAPI at user scope protects these at rest and from other users
// of this machine. It does NOT isolate them from code running as the same Windows user.
// SECURITY.md says so in those words.

const (
	vaultFilePrefix = ".agentb-credential-"
	recordFile      = ".agentb-credentials.json"
)

// Entry is what Settings lists and what a binding is checked against. There is no value
// in it, by construction: a listing cannot leak what it does not carry.
type Entry struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Origin   string `json:"origin"`
	Header   string `json:"header,omitempty"`
	StoredAt string `json:"stored_at"`
}

type EntraDefinition struct {
	Tenant   string   `json:"tenant"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes"`
}

// Vault is the named-credential store under one data root.
type Vault struct {
	root string
	mu   sync.Mutex
}

func NewVault(dataRoot string) *Vault {
	if dataRoot == "" {
		dataRoot = "."
	}
	return &Vault{root: dataRoot}
}

func (v *Vault) store(name string) (*Store, error) {
	if !credentialName.MatchString(name) {
		return nil, fmt.Errorf("a credential name is lower-case letters, digits and hyphens: %q is not", name)
	}
	return &Store{
		path:       filepath.Join(v.root, vaultFilePrefix+name+".dpapi"),
		tempPrefix: vaultFilePrefix + name + "-*",
	}, nil
}

func (v *Vault) recordPath() string { return filepath.Join(v.root, recordFile) }

func (v *Vault) readRecords() (map[string]Entry, error) {
	raw, err := os.ReadFile(v.recordPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the credential record: %w", err)
	}
	records := map[string]Entry{}
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, fmt.Errorf("the credential record is unreadable: %w", err)
	}
	return records, nil
}

func (v *Vault) writeRecords(records map[string]Entry) error {
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(v.root, 0o755); err != nil {
		return err
	}
	temporary := v.recordPath() + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, v.recordPath())
}

// List is what Settings shows: every credential by name, kind, origin and date.
func (v *Vault) List() ([]Entry, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	records, err := v.readRecords()
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(records))
	for _, entry := range records {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// Put stores a secret and its binding. The secret is written first and read back before
// the record is written, so a record never claims a credential the store does not hold.
func (v *Vault) Put(name, origin, header, secret string) error {
	origin, err := NormalizeOrigin(origin)
	if err != nil {
		return err
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("the credential is empty")
	}
	if err := validHeader(header); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	store, err := v.store(name)
	if err != nil {
		return err
	}
	if err := store.Write([]byte(secret)); err != nil {
		return err
	}
	// (h)'s rule, applied to every write and not only to a migration: what was stored is
	// read back before anything is told it exists.
	readBack, err := store.Read()
	if err != nil {
		return fmt.Errorf("the credential was stored but did not read back: %w", err)
	}
	if string(readBack) != secret {
		_ = store.Clear()
		return errors.New("the credential did not read back as it was written")
	}
	records, err := v.readRecords()
	if err != nil {
		return err
	}
	records[name] = Entry{Name: name, Kind: "key", Origin: origin, Header: strings.TrimSpace(header), StoredAt: time.Now().UTC().Format(time.RFC3339)}
	return v.writeRecords(records)
}

func (v *Vault) PutEntra(name, origin, tenant, clientID string, scopes []string) error {
	definition := EntraDefinition{Tenant: strings.TrimSpace(tenant), ClientID: strings.TrimSpace(clientID)}
	for _, scope := range scopes {
		if scope = strings.TrimSpace(scope); scope != "" {
			definition.Scopes = append(definition.Scopes, scope)
		}
	}
	if definition.Tenant == "" || definition.ClientID == "" || len(definition.Scopes) == 0 {
		return errors.New("tenant, client id and at least one scope are required")
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		return err
	}
	if err := v.Put(name, origin, "", string(encoded)); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	records, err := v.readRecords()
	if err != nil {
		return err
	}
	entry := records[name]
	entry.Kind = "entra"
	records[name] = entry
	if err := v.writeRecords(records); err != nil {
		return err
	}
	cache, err := v.EntraCache(name)
	if err != nil {
		return err
	}
	if err := cache.Clear(); err != nil && !errors.Is(err, ErrNotStored) {
		return err
	}
	return nil
}

func (v *Vault) Entra(name string) (EntraDefinition, Entry, error) {
	secret, entry, err := v.Secret(name)
	if err != nil {
		return EntraDefinition{}, Entry{}, err
	}
	if entry.Kind != "entra" {
		return EntraDefinition{}, Entry{}, fmt.Errorf("credential %q is not an Entra credential", name)
	}
	var definition EntraDefinition
	if json.Unmarshal([]byte(secret), &definition) != nil {
		return EntraDefinition{}, Entry{}, fmt.Errorf("Entra credential %q is unreadable", name)
	}
	return definition, entry, nil
}

func (v *Vault) EntraCache(name string) (*Store, error) {
	return NewNamed(v.root, "entra-"+name+"-cache")
}

// Rebind changes where a credential may go, without touching the secret. (e) means the
// operator has approved this before it is called.
func (v *Vault) Rebind(name, origin, header string) error {
	origin, err := NormalizeOrigin(origin)
	if err != nil {
		return err
	}
	if err := validHeader(header); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	records, err := v.readRecords()
	if err != nil {
		return err
	}
	entry, present := records[name]
	if !present {
		return fmt.Errorf("no credential named %q is stored", name)
	}
	entry.Origin = origin
	entry.Header = strings.TrimSpace(header)
	records[name] = entry
	return v.writeRecords(records)
}

// Delete removes the record and the secret. Immediate, as (c) says.
func (v *Vault) Delete(name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	store, err := v.store(name)
	if err != nil {
		return err
	}
	if err := store.Clear(); err != nil && !errors.Is(err, ErrNotStored) {
		return err
	}
	records, err := v.readRecords()
	if err != nil {
		return err
	}
	if records[name].Kind == "entra" {
		if cache, cacheErr := v.EntraCache(name); cacheErr == nil {
			_ = cache.Clear()
		}
	}
	delete(records, name)
	return v.writeRecords(records)
}

// Secret is the ONLY way a value leaves this package, and it hands back the binding with
// it so the caller cannot use one without the other.
func (v *Vault) Secret(name string) (string, Entry, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	records, err := v.readRecords()
	if err != nil {
		return "", Entry{}, err
	}
	entry, present := records[name]
	if !present {
		return "", Entry{}, fmt.Errorf("no credential named %q is stored", name)
	}
	store, err := v.store(name)
	if err != nil {
		return "", Entry{}, err
	}
	value, err := store.Read()
	if err != nil {
		return "", Entry{}, err
	}
	return string(value), entry, nil
}

// Has says whether a name is bound, for validation that must not read a secret.
func (v *Vault) Has(name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	records, err := v.readRecords()
	if err != nil {
		return false
	}
	_, present := records[name]
	return present
}

func validHeader(header string) error {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	if strings.EqualFold(header, "Host") {
		return errors.New("a credential cannot be sent as the Host header")
	}
	for _, r := range header {
		if r <= ' ' || r == ':' || r >= 0x7f {
			return fmt.Errorf("%q is not a header name", header)
		}
	}
	return nil
}
