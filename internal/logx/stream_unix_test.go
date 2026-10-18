//go:build unix

package logx

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestJournalStreamDetection(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	t.Setenv("JOURNAL_STREAM", fmt.Sprintf("%d:%d", uint64(st.Dev), uint64(st.Ino)))
	if !isJournalStream(f) {
		t.Error("matching JOURNAL_STREAM not detected")
	}
	if isJournalStream(&bytes.Buffer{}) {
		t.Error("non-file writer treated as journal")
	}
	t.Setenv("JOURNAL_STREAM", fmt.Sprintf("%d:%d", uint64(st.Dev), uint64(st.Ino)+1))
	if isJournalStream(f) {
		t.Error("mismatched inode treated as journal")
	}
	if _, ok := NewHandler(f).(*textHandler); !ok {
		t.Error("non-journal stderr did not get the text handler")
	}
	t.Setenv("JOURNAL_STREAM", "")
	if isJournalStream(f) {
		t.Error("empty JOURNAL_STREAM treated as journal")
	}
}
