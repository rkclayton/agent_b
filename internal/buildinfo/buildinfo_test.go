package buildinfo

import "testing"

func TestCurrentUsesInjectedCleanAndDirtyIdentity(t *testing.T) {
	oldCommit, oldDirty := Commit, Dirty
	t.Cleanup(func() { Commit, Dirty = oldCommit, oldDirty })
	Commit = "0123456789abcdef0123456789abcdef01234567"

	Dirty = "false"
	clean := Current()
	if clean.Display != "0123456789ab" || clean.Dirty || !clean.Known || clean.Source != "ldflags" {
		t.Fatalf("clean identity = %+v", clean)
	}

	Dirty = "true"
	dirty := Current()
	if dirty.Display != "0123456789ab+dirty" || !dirty.Dirty || !dirty.Known || dirty.Source != "ldflags" {
		t.Fatalf("dirty identity = %+v", dirty)
	}
}
