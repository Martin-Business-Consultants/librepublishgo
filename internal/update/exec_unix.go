//go:build unix

package update

import (
	"os"
	"syscall"
)

// Exec becomes bin, with this process's arguments and environment: the
// same process (and so the same container's main process), a new binary.
func Exec(bin string) error {
	return syscall.Exec(bin, append([]string{bin}, os.Args[1:]...), os.Environ()) //nolint:gosec // a release checked against its checksum
}
