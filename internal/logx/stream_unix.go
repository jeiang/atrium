//go:build unix

package logx

import (
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func isJournalStream(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	dev, ino, ok := strings.Cut(os.Getenv("JOURNAL_STREAM"), ":")
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return strconv.FormatUint(uint64(st.Dev), 10) == dev && strconv.FormatUint(st.Ino, 10) == ino
}
