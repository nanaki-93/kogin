//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package sandbox

import (
	"fmt"
	"os/exec"
)

// The runner refuses to start where Kogin cannot own descendants.
func ConfigureCommand(_ *exec.Cmd) error {
	return fmt.Errorf("project command execution is unavailable: descendant cancellation is unsupported on this platform")
}
