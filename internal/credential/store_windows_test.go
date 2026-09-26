//go:build windows

package credential

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestStoreRoundTripAndClear(t *testing.T) {
	dataRoot := t.TempDir()
	store := New(dataRoot)
	if store.Path() != filepath.Join(dataRoot, FileName) {
		t.Fatalf("credential path = %q, want data-root path", store.Path())
	}
	if _, err := store.Read(); !errors.Is(err, ErrNotStored) {
		t.Fatalf("absent Read error = %v, want ErrNotStored", err)
	}
	if status := store.Status(); status.Stored || status.StoredAt != "" {
		t.Fatalf("absent status = %+v", status)
	}

	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(password); err != nil {
		t.Fatal(err)
	}
	if status := store.Status(); !status.Stored || status.StoredAt == "" {
		t.Fatalf("stored status = %+v", status)
	}
	got, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, password) {
		t.Fatal("credential did not round-trip")
	}
	if bytes.Equal(got, mustReadFile(t, store.Path())) {
		t.Fatal("credential store contains plaintext")
	}
	second := make([]byte, 32)
	if _, err := rand.Read(second); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(second); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err = store.Read()
	if err != nil || !bytes.Equal(got, second) {
		t.Fatalf("replacement round trip: got %x, err %v", got, err)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("second Clear: %v", err)
	}
	if _, err := store.Read(); !errors.Is(err, ErrNotStored) {
		t.Fatalf("Read after Clear error = %v, want ErrNotStored", err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Item 2li (g). Three of the four assertions the item asks for can be made
// without elevation; the fourth -- provisioned by SYSTEM, used by a different
// non-admin user -- cannot, and rel-1.15.0/W0 recorded why.

// (a), enforced rather than documented: a connection credential has no machine
// path, so there is nothing for an API key to write to.
func TestMachineScopeIsNotAvailableToAnAPIKey(t *testing.T) {
	dataRoot := t.TempDir()
	key, err := NewNamed(dataRoot, "some-connection")
	if err != nil {
		t.Fatalf("NewNamed: %v", err)
	}
	if key.MachinePath() != "" {
		t.Fatalf("a connection credential has a machine path %q", key.MachinePath())
	}
	if err := key.WriteMachine([]byte("an API key")); !errors.Is(err, ErrScopeNotAvailable) {
		t.Fatalf("WriteMachine on a connection credential = %v, want ErrScopeNotAvailable", err)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, MachineFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused machine write left a file behind")
	}
}

// The machine scope round-trips, the file carries the protected access list the
// trade in (b) depends on, and Status says which scope answered.
func TestMachineScopeRoundTripsUnderItsOwnAccessList(t *testing.T) {
	dataRoot := t.TempDir()
	store := New(dataRoot).ForAccount(serviceAccountForTest(t))
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := store.WriteMachine(secret); err != nil {
		t.Fatalf("WriteMachine: %v", err)
	}
	if status := store.Status(); !status.Stored || status.Scope != "machine" {
		t.Fatalf("status = %+v, want a stored machine-scoped credential", status)
	}
	read, err := store.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(read, secret) {
		t.Fatal("the machine-scoped credential did not round-trip")
	}
	// (e): a user-scoped credential this user stored still answers first.
	if err := store.Write([]byte("the operator's own")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if status := store.Status(); status.Scope != "user" {
		t.Fatalf("scope = %q after a user-scoped write, want user", status.Scope)
	}
	// Clear speaks for this user only: an administrator's machine credential
	// survives it, and ClearMachine is what (f) reaches for.
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Clear left the user-scoped credential behind")
	}
	if _, err := os.Stat(filepath.Join(dataRoot, MachineFileName)); err != nil {
		t.Fatalf("Clear removed the machine credential this user does not own: %v", err)
	}
	if err := store.ClearMachine(); err != nil {
		t.Fatalf("ClearMachine: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, MachineFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ClearMachine left the machine credential behind")
	}
}

// (b), the whole point: widen the access list and the blob stops being usable.
// Granting a principal read on a file this account owns needs no elevation,
// which is why this assertion can run where the SYSTEM half cannot.
func TestAWidenedAccessListRefusesTheMachineBlob(t *testing.T) {
	dataRoot := t.TempDir()
	store := New(dataRoot).ForAccount(serviceAccountForTest(t))
	if err := store.WriteMachine([]byte("a service password")); err != nil {
		t.Fatalf("WriteMachine: %v", err)
	}
	if _, err := store.Read(); err != nil {
		t.Fatalf("the blob does not read before its access list is widened: %v", err)
	}
	widenForTest(t, filepath.Join(dataRoot, MachineFileName))
	_, err := store.Read()
	if !errors.Is(err, ErrMachineBlobUnprotected) {
		t.Fatalf("Read after widening = %v, want ErrMachineBlobUnprotected", err)
	}
}

// widenForTest puts an access list on the blob that still protects the DACL but
// lets Everyone read it -- the realistic shape of the mistake (b) exists to
// catch. The parent directory is untouched, so the temporary tree still cleans
// up through its own delete-child right.
func widenForTest(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;FR;;;WD)")
	if err != nil {
		t.Fatalf("build the widened descriptor: %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("read the widened descriptor: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatalf("widen the access list: %v", err)
	}
}

// serviceAccountForTest casts the account running the test as the service
// account whose credential this is -- the third principal the access list
// allows. Without it the test could write the blob and then be denied by its
// own protection, which is the design working, not a failure.
func serviceAccountForTest(t *testing.T) string {
	t.Helper()
	name := os.Getenv("USERNAME")
	if name == "" {
		t.Skip("no account name to stand in for the service account")
	}
	return name
}
