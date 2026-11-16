package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// STORAGE, and only storage. A vault credential is kept exactly the way
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

type GoogleDefinition struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
}

// Vault is the named-credential store under one data root.
type Vault struct {
	root        string
	mu          sync.Mutex
	logins      map[string]Entry
	secretReads atomic.Uint64
	entries     map[string]Entry
}

func NewVault(dataRoot string) *Vault {
	if dataRoot == "" {
		dataRoot = "."
	}
	return &Vault{root: dataRoot}
}

// SecretReads counts vault acquisitions without retaining names or values.
func (v *Vault) SecretReads() uint64 { return v.secretReads.Load() }

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
	v.logins = nil
	v.entries = nil
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

// Lookup reads metadata once per vault change, never decrypting a credential.
func (v *Vault) Lookup(name string) (Entry, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.lookup(name)
}
func (v *Vault) lookup(name string) (Entry, error) {
	if v.entries == nil {
		records, err := v.readRecords()
		if err != nil {
			return Entry{}, err
		}
		v.entries = records
	}
	entry, ok := v.entries[name]
	if !ok {
		return Entry{}, fmt.Errorf("no credential named %q is stored", name)
	}
	return entry, nil
}

// Login metadata is indexed once; password requests read one DPAPI record.
func (v *Vault) loginIndex() error {
	if v.logins != nil {
		return nil
	}
	records, err := v.readRecords()
	if err != nil {
		return err
	}
	v.logins = make(map[string]Entry)
	for _, entry := range records {
		if entry.Kind == "login" {
			v.logins[entry.Origin] = entry
		}
	}
	return nil
}

func loginOrigin(user, host, port string) (string, error) {
	if port == "" {
		port = "22"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || user == "" || host == "" || strings.ContainsAny(user, "@\r\n\x00 /\\") || strings.ContainsAny(host, "@\r\n\x00 /\\") {
		return "", errors.New("a login needs a user, host and port from 1 to 65535")
	}
	return user + "@" + host + ":" + strconv.Itoa(n), nil
}

func (v *Vault) PutLogin(name, user, host, port, password string) error {
	origin, err := loginOrigin(user, host, port)
	if err != nil {
		return err
	}
	if password == "" || len(password) > 16384 || strings.ContainsAny(password, "\x00\r\n") {
		return errors.New("a login needs a single-line password")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err = v.loginIndex(); err != nil {
		return err
	}
	if existing, ok := v.logins[origin]; ok && existing.Name != name {
		return errors.New("a login for that user, host and port is already stored")
	}
	if len(v.logins) >= 256 {
		if _, ok := v.logins[origin]; !ok {
			return errors.New("the 256 stored logins are full")
		}
	}
	store, err := v.store(name)
	if err != nil {
		return err
	}
	records, err := v.readRecords()
	if err != nil {
		return err
	}
	if old, ok := records[name]; ok && old.Kind != "login" {
		return errors.New("that name belongs to another credential kind")
	}
	if err = store.Write([]byte(password)); err != nil {
		return err
	}
	check, err := store.Read()
	if err != nil {
		return err
	}
	if string(check) != password {
		return errors.New("the login did not read back as stored")
	}
	records[name] = Entry{Name: name, Kind: "login", Origin: origin, StoredAt: time.Now().UTC().Format(time.RFC3339)}
	return v.writeRecords(records)
}

func (v *Vault) LoginName(user, host string, port int) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.loginIndex(); err != nil {
		return "", err
	}
	origin, err := loginOrigin(user, host, strconv.Itoa(port))
	if err != nil {
		return "", err
	}
	return v.logins[origin].Name, nil
}

func (v *Vault) LoginPassword(name, user, host string, port int) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.loginIndex(); err != nil {
		return nil, err
	}
	origin, err := loginOrigin(user, host, strconv.Itoa(port))
	if err != nil {
		return nil, err
	}
	if entry, ok := v.logins[origin]; !ok || entry.Name != name {
		return nil, errors.New("login binding changed")
	}
	store, err := v.store(name)
	if err != nil {
		return nil, err
	}
	v.secretReads.Add(1)
	return store.Read()
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
	return v.putEntra(name, origin, tenant, clientID, scopes, false)
}

// PutEntraIfAbsent creates a discovered public-client definition without ever
// replacing a credential the operator may have created concurrently in Settings.
func (v *Vault) PutEntraIfAbsent(name, origin, tenant, clientID string, scopes []string) error {
	return v.putEntra(name, origin, tenant, clientID, scopes, true)
}

func (v *Vault) putEntra(name, origin, tenant, clientID string, scopes []string, requireAbsent bool) error {
	origin, err := NormalizeOrigin(origin)
	if err != nil {
		return err
	}
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
	v.mu.Lock()
	defer v.mu.Unlock()
	records, err := v.readRecords()
	if err != nil {
		return err
	}
	if _, exists := records[name]; requireAbsent && exists {
		return fmt.Errorf("credential %q already exists", name)
	}
	store, err := v.store(name)
	if err != nil {
		return err
	}
	if err := store.Write(encoded); err != nil {
		return err
	}
	readBack, err := store.Read()
	if err != nil {
		return fmt.Errorf("the Entra definition was stored but did not read back: %w", err)
	}
	if string(readBack) != string(encoded) {
		_ = store.Clear()
		return errors.New("the Entra definition did not read back as it was written")
	}
	records[name] = Entry{Name: name, Kind: "entra", Origin: origin, StoredAt: time.Now().UTC().Format(time.RFC3339)}
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

func (v *Vault) PutGoogle(name, origin, clientID, clientSecret string, scopes []string) error {
	origin, err := NormalizeOrigin(origin)
	if err != nil {
		return err
	}
	parsedOrigin, _ := url.Parse(origin)
	host := strings.ToLower(parsedOrigin.Hostname())
	if host != "www.googleapis.com" && !strings.HasSuffix(host, ".googleapis.com") {
		return errors.New("a Google credential origin must be www.googleapis.com or a narrower googleapis.com API host")
	}
	definition := GoogleDefinition{ClientID: strings.TrimSpace(clientID), ClientSecret: strings.TrimSpace(clientSecret)}
	for _, scope := range scopes {
		if scope = strings.TrimSpace(scope); scope != "" {
			definition.Scopes = append(definition.Scopes, scope)
		}
	}
	if definition.ClientID == "" || definition.ClientSecret == "" || len(definition.Scopes) == 0 {
		return errors.New("client id, client secret and at least one scope are required")
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	store, err := v.store(name)
	if err != nil {
		return err
	}
	if err := store.Write(encoded); err != nil {
		return err
	}
	readBack, err := store.Read()
	if err != nil {
		return fmt.Errorf("the Google definition was stored but did not read back: %w", err)
	}
	if string(readBack) != string(encoded) {
		_ = store.Clear()
		return errors.New("the Google definition did not read back as it was written")
	}
	records, err := v.readRecords()
	if err != nil {
		return err
	}
	records[name] = Entry{Name: name, Kind: "google", Origin: origin, StoredAt: time.Now().UTC().Format(time.RFC3339)}
	if err := v.writeRecords(records); err != nil {
		return err
	}
	cache, err := v.GoogleCache(name)
	if err != nil {
		return err
	}
	if err := cache.Clear(); err != nil && !errors.Is(err, ErrNotStored) {
		return err
	}
	return nil
}

func (v *Vault) Google(name string) (GoogleDefinition, Entry, error) {
	secret, entry, err := v.Secret(name)
	if err != nil {
		return GoogleDefinition{}, Entry{}, err
	}
	if entry.Kind != "google" {
		return GoogleDefinition{}, Entry{}, fmt.Errorf("credential %q is not a Google credential", name)
	}
	var definition GoogleDefinition
	if json.Unmarshal([]byte(secret), &definition) != nil {
		return GoogleDefinition{}, Entry{}, fmt.Errorf("Google credential %q is unreadable", name)
	}
	return definition, entry, nil
}

func (v *Vault) GoogleCache(name string) (*Store, error) {
	return NewNamed(v.root, "google-"+name+"-cache")
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
	if records[name].Kind == "google" {
		if cache, cacheErr := v.GoogleCache(name); cacheErr == nil {
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
	entry, err := v.lookup(name)
	if err != nil {
		return "", Entry{}, err
	}
	store, err := v.store(name)
	if err != nil {
		return "", Entry{}, err
	}
	v.secretReads.Add(1)
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
