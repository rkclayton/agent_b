package credential

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoginBound2yd(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI")
	}
	v := NewVault(t.TempDir())
	for i := 0; i < 256; i++ {
		if e := v.PutLogin(fmt.Sprintf("login-%d", i), "someone", "example.org", fmt.Sprint(i+1), "fixture-secret"); e != nil {
			t.Fatal(e)
		}
	}
	if e := v.PutLogin("overflow", "someone-else", "example.org", "22", "fixture-secret"); e == nil {
		t.Fatal("unbounded logins")
	}
	v.LoginName("someone", "example.org", 256)
	start := time.Now()
	for i := 0; i < 10000; i++ {
		name, e := v.LoginName("someone", "example.org", 256)
		if e != nil || name != "login-255" {
			t.Fatal(name, e)
		}
	}
	t.Logf("full bound=256; 10000 indexed binding lookups=%s", time.Since(start))
}

func TestOldCredentialRecordsAndSignInCachesArePreserved2zi(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows protects the fixture records")
	}
	root := t.TempDir()
	v := NewVault(root)
	if err := v.Put("fixture-key", "https://api.example.test", "", "fixture-key-value"); err != nil {
		t.Fatal(err)
	}
	if err := v.PutLogin("fixture-login", "someone", "example.org", "22", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	if err := v.PutEntra("fixture-entra", "https://identity.example.test", "fixture-tenant", "fixture-client", []string{"fixture.read"}); err != nil {
		t.Fatal(err)
	}
	if err := v.PutGoogle("fixture-google", "https://gmail.googleapis.com", "fixture-client", "fixture-client-secret", []string{"fixture.read"}); err != nil {
		t.Fatal(err)
	}
	ms, _ := v.EntraCache("fixture-entra")
	google, _ := v.GoogleCache("fixture-google")
	for _, cache := range []*Store{ms, google} {
		if err := cache.Write([]byte("fixture-refresh-cache")); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := v.List()
	if err != nil || len(entries) != 4 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	legacy := make(map[string]Entry)
	for _, entry := range entries {
		entry.Origins, entry.Provider = nil, ""
		legacy[entry.Name] = entry
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v.recordPath(), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	v = NewVault(root)
	for name, want := range legacy {
		entry, err := v.Lookup(name)
		if err != nil || entry.Kind != want.Kind || len(entry.Destinations()) != 1 || entry.Destinations()[0] != want.Origin {
			t.Fatalf("retrieved entry=%+v err=%v", entry, err)
		}
	}
	if err := v.PutLoginOrigins("fixture-login", []string{"someone@example.org:22", "someone@second.example.org:2222"}, "fixture-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.LoginPassword("fixture-login", "someone", "second.example.org", 2222); err != nil {
		t.Fatal(err)
	}
	if _, err := v.LoginPassword("fixture-login", "someone", "third.example.org", 22); err == nil {
		t.Fatal("unlisted login address was accepted")
	}
	for _, cache := range []*Store{ms, google} {
		data, err := cache.Read()
		if err != nil || string(data) != "fixture-refresh-cache" {
			t.Fatal("the credential metadata upgrade changed a sign-in cache")
		}
	}
	index, err := os.ReadFile(v.recordPath())
	if err != nil || strings.Count(string(index), `"kind": "sign-in"`) != 2 || strings.Contains(string(index), "fixture-client-secret") {
		t.Fatal("the index did not use the new sign-in shape")
	}
}

func TestStoredLogin2yd(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI")
	}
	v := NewVault(t.TempDir())
	if err := v.PutLogin("example-login", "someone", "example.org", "", "planted-login-secret"); err != nil {
		t.Fatal(err)
	}
	entries, err := v.List()
	if err != nil || len(entries) != 1 || entries[0].Kind != "login" || entries[0].Origin != "someone@example.org:22" {
		t.Fatalf("listing: %v %v", entries, err)
	}
	data, _ := json.Marshal(entries)
	if strings.Contains(string(data), "planted-login-secret") {
		t.Fatal("secret in listing")
	}
	name, err := v.LoginName("someone", "example.org", 22)
	if err != nil || name != "example-login" {
		t.Fatal(name, err)
	}
	for _, values := range [][3]string{{"someone-else", "example.org", "22"}, {"someone", "other.example", "22"}, {"someone", "example.org", "23"}} {
		port := 22
		if values[2] == "23" {
			port = 23
		}
		if n, _ := v.LoginName(values[0], values[1], port); n != "" {
			t.Fatal("mismatched login")
		}
	}
	if err = v.Delete("example-login"); err != nil {
		t.Fatal(err)
	}
	if n, _ := v.LoginName("someone", "example.org", 22); n != "" {
		t.Fatal("deleted login")
	}
}

// Item 2nv (a): STORAGE and DESTINATION ENFORCEMENT are two parts with a narrow seam, and
// these are their cases. Storage keeps a secret the way the rest of this package already
// does — a per-name blob under the data root, user scope — and adds only the record of
// what it is bound to. Enforcement never touches storage at all: it is given an origin and
// a URL and answers.

func TestAnOriginIsSchemeHostAndPort2nv(t *testing.T) {
	for raw, want := range map[string]string{
		"https://api.example.test:8443":         "https://api.example.test:8443",
		"https://api.example.test":              "https://api.example.test:443",
		"https://API.Example.Test:8443/ignored": "https://api.example.test:8443",
	} {
		got, err := NormalizeOrigin(raw)
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("%s normalized to %s, want %s", raw, got, want)
		}
	}
	// (f): HTTPS only, and nothing that hides a second destination inside the first.
	for _, bad := range []string{
		"http://api.example.test", "api.example.test", "https://", "wss://api.example.test",
		"https://user:someone@example.org", "https://api.example.test:notaport",
	} {
		if got, err := NormalizeOrigin(bad); err == nil {
			t.Errorf("%q was accepted as the origin %s", bad, got)
		}
	}
}

// (f) is the whole of this: the credential goes to ONE origin and nowhere else.
func TestACredentialGoesOnlyToItsOrigin2nv(t *testing.T) {
	const origin = "https://api.example.test:8443"
	allowed := []string{
		"https://api.example.test:8443/v1/queue",
		"https://api.example.test:8443/",
	}
	for _, address := range allowed {
		target, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}
		if err := AllowsOrigin(origin, target); err != nil {
			t.Errorf("%s was refused: %v", address, err)
		}
	}
	refused := map[string]string{
		"http://api.example.test:8443/v1":     "scheme",
		"https://api.example.test/v1":         "port",
		"https://api.example.test:443/v1":     "port",
		"https://api.example.test:9443/v1":    "port",
		"https://other.example.test:8443/":    "host",
		"https://api.example.test.evil:8443/": "host",
	}
	for address, why := range refused {
		target, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}
		err = AllowsOrigin(origin, target)
		if err == nil {
			t.Errorf("%s was allowed, and it differs by %s", address, why)
			continue
		}
		if !strings.Contains(err.Error(), origin) {
			t.Errorf("the refusal for %s does not name the approved origin: %v", address, err)
		}
	}
}

// (b) and (c): what the vault keeps, and what it never gives back to a listing.
func TestTheVaultStoresTheSecretAndListsOnlyItsRecord2nv(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIPPED: credential storage is DPAPI, so this case is Windows-only.")
	}
	root := t.TempDir()
	vault := NewVault(root)
	const secret = "planted-secret-value-2nv"
	if err := vault.Put("depot", "https://api.example.test:8443", "X-Depot-Key", secret); err != nil {
		t.Fatal(err)
	}
	entries, err := vault.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entries", len(entries))
	}
	entry := entries[0]
	if entry.Name != "depot" || entry.Origin != "https://api.example.test:8443" || entry.Header != "X-Depot-Key" {
		t.Fatalf("%+v", entry)
	}
	if entry.StoredAt == "" {
		t.Error("the entry does not say when it was stored")
	}
	// The listing carries no value, and neither does anything printed from it.
	if strings.Contains(entry.Name+entry.Origin+entry.Header+entry.Kind+entry.StoredAt, secret) {
		t.Fatal("the listing carries the secret")
	}
	// Nor does the record on disk: the metadata is plain, so it must hold nothing.
	record, err := os.ReadFile(filepath.Join(root, recordFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record), secret) {
		t.Fatal("the metadata file carries the secret in plain text")
	}
	// And the blob is not plain text either.
	blob, err := os.ReadFile(filepath.Join(root, vaultFilePrefix+"depot.dpapi"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), secret) {
		t.Fatal("the stored blob is plain text")
	}

	value, read, err := vault.Secret("depot")
	if err != nil {
		t.Fatal(err)
	}
	if value != secret || read.Origin != entry.Origin {
		t.Fatalf("the secret did not read back: %q %+v", value, read)
	}

	// Rebinding changes the record and keeps the secret.
	if err := vault.Rebind("depot", "https://api.example.test:9443", ""); err != nil {
		t.Fatal(err)
	}
	value, read, err = vault.Secret("depot")
	if err != nil || value != secret || read.Origin != "https://api.example.test:9443" || read.Header != "" {
		t.Fatalf("after rebinding: %q %+v %v", value, read, err)
	}

	// Removing one is immediate, and takes the blob with it.
	if err := vault.Delete("depot"); err != nil {
		t.Fatal(err)
	}
	if entries, err := vault.List(); err != nil || len(entries) != 0 {
		t.Fatalf("%d entries after removal (%v)", len(entries), err)
	}
	if _, err := os.Stat(filepath.Join(root, vaultFilePrefix+"depot.dpapi")); !os.IsNotExist(err) {
		t.Fatal("the blob outlived its record")
	}
	if _, _, err := vault.Secret("depot"); err == nil {
		t.Fatal("a removed credential still reads")
	}
}

// A name is a slug, an origin is required, and a credential cannot be stored empty.
func TestTheVaultRefusesWhatItCannotKeepSafely2nv(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIPPED: credential storage is DPAPI, so this case is Windows-only.")
	}
	vault := NewVault(t.TempDir())
	for _, test := range []struct{ name, origin, secret, why string }{
		{"Depot", "https://api.example.test", "x", "an upper-case name"},
		{"depot key", "https://api.example.test", "x", "a name with a space"},
		{"depot", "http://api.example.test", "x", "a plain-http origin"},
		{"depot", "", "x", "no origin"},
		{"depot", "https://api.example.test", "", "an empty secret"},
	} {
		if err := vault.Put(test.name, test.origin, "", test.secret); err == nil {
			t.Errorf("the vault accepted %s", test.why)
		}
	}
}

func TestCredentialMetadataLookupDoesNotReloadCatalog2ye(t *testing.T) {
	vault := NewVault(t.TempDir())
	records := map[string]Entry{}
	for n := 0; n < 4096; n++ {
		name := fmt.Sprintf("key-%d", n)
		records[name] = Entry{Name: name, Kind: "key", Origin: "https://example.org:443"}
	}
	if err := vault.writeRecords(records); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Lookup("key-4095"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(vault.recordPath(), vault.recordPath()+".held"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for n := 0; n < 1000000; n++ {
		if _, err := vault.Lookup("key-4095"); err != nil {
			t.Fatal("per-use catalog read", err)
		}
	}
	t.Logf("4096 catalog records, 1000000 metadata lookups: %s", time.Since(start))
	records["key-4095"] = Entry{Name: "key-4095", Kind: "entra", Origin: "https://example.org:443"}
	if err := vault.writeRecords(records); err != nil {
		t.Fatal(err)
	}
	entry, err := vault.Lookup("key-4095")
	if err != nil || entry.Kind != "entra" {
		t.Fatal("stale metadata", entry, err)
	}
}

func TestPublishedEntraDefinitionCannotReplaceAnExistingCredential(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIPPED: credential storage is DPAPI, so this case is Windows-only.")
	}
	vault := NewVault(t.TempDir())
	if err := vault.PutEntra(
		"work-api",
		"https://api.example.test",
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		[]string{"scope.read"}); err != nil {
		t.Fatal(err)
	}
	if err := vault.PutEntraIfAbsent(
		"work-api",
		"https://api.example.test",
		"33333333-3333-3333-3333-333333333333",
		"44444444-4444-4444-4444-444444444444",
		[]string{"scope.write"}); err == nil {
		t.Fatal("published definition replaced an existing credential")
	}
	definition, _, err := vault.Entra("work-api")
	if err != nil || definition.ClientID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("definition=%+v err=%v", definition, err)
	}
}
