//go:build projector_slow

package projectorpins

import "testing"

func TestProjectorGoldenMastersSlow(t *testing.T) {
	root, err := RepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySlow(root); err != nil {
		t.Fatal(err)
	}
}
