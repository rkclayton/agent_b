//go:build !windows

package credential

func protect([]byte) ([]byte, error)        { return nil, ErrUnsupported }
func protectMachine([]byte) ([]byte, error) { return nil, ErrUnsupported }
func unprotect([]byte) ([]byte, error)      { return nil, ErrUnsupported }

// Item 2li (b): there is no machine scope off Windows, so there is no access
// list to hold. Both halves refuse rather than pretend, which keeps the
// non-Windows build honest about a guarantee it cannot make.
func secureMachineBlob(string, string) error   { return ErrUnsupported }
func machineBlobACLHolds(string, string) error { return ErrUnsupported }
