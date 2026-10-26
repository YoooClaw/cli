package transfer

import (
	"golang.org/x/sys/unix"
	"os"
)

func renameDirectoryExclusive(parent *os.File, from, to string) error {
	return unix.RenameatxNp(int(parent.Fd()), from, int(parent.Fd()), to, unix.RENAME_EXCL)
}
