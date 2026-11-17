//go:build darwin

package mediaagent

import "golang.org/x/sys/unix"

func renameExclusive(a int, b string, c int, d string) error {
	return unix.RenameatxNp(a, b, c, d, unix.RENAME_EXCL)
}
