//go:build !windows

package quietproc

import "os/exec"

// Quiet is a no-op away from Windows: there is no window to hide, and the product is
// Windows-first. It exists so a caller does not need a build tag of its own.
func Quiet(command *exec.Cmd) *exec.Cmd { return command }
