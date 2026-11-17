//go:build linux

package mediaagent

import "golang.org/x/sys/unix"

func renameExclusive(a int, b string, c int, d string) error {
	return unix.Renameat2(a, b, c, d, unix.RENAME_NOREPLACE)
}
