package dbcmd

import (
	"fmt"
	"os"
	"syscall"
)

func chownLike(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if err := os.Chown(path, int(st.Uid), int(st.Gid)); err != nil {
		return fmt.Errorf("give the new database file the owner of %s: %w", path, err)
	}
	return nil
}
