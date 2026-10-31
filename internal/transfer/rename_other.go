//go:build !linux && !darwin

package transfer

import "os"

// Fail closed where atomic no-replace directory rename is unavailable.
func renameDirectoryExclusive(parent *os.File, from, to string) error {
	return fail("SKILL_ATOMIC_INSTALL_UNSUPPORTED")
}
