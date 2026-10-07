package credential

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
