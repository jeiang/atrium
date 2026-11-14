package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/passrun"
)

func dotFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out
}

func stagedSize(dir, prefix string) int64 {
	entries, _ := os.ReadDir(dir)
	var size int64
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if info, err := e.Info(); err == nil {
			size = max(size, info.Size())
		}
	}
	return size
}

func TestBackupInterruptedMidWriteLeavesNoTempFilesAndTheNextRunSucceeds(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	const bigFiles, bigSize = 8, int64(128 << 20)
	for i := range bigFiles {
		path := filepath.Join(dir, "files", "big", fmt.Sprintf("%d.bin", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(bigSize); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	dest := t.TempDir()
	setEnv(t, "ATRIUM_HOST_CONFIG", backupHost(t, dir, "backup_directory = \""+dest+"\""))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)

	manual := filepath.Join(dest, "atrium-manual.backup")
	cmd := cliExitProcess(t, "backup "+manual)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	prefix := "." + filepath.Base(manual) + "."
	var exit error
	signaled := false
	var largest int64
	for waiting := true; waiting; {
		select {
		case exit = <-done:
			waiting = false
			continue
		case <-time.After(time.Millisecond):
		}
		size := stagedSize(dest, prefix)
		largest = max(largest, size)
		if !signaled && size > 1<<20 {
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			signaled = true
		}
	}
	if !signaled {
		t.Fatalf("the archive write ended before it could be interrupted: %v, stdout %q, stderr %q", exit, stdout.String(), stderr.String())
	}
	if code := exitCode(exit); code == cli.ExitOK {
		t.Fatalf("backup interrupted mid-write exited 0, stdout %q", stdout.String())
	}
	if total := int64(bigFiles) * bigSize; largest >= total/2 {
		t.Fatalf("the interrupted archive grew to %d of %d bytes; the write did not stop at the signal", largest, total)
	}
	if _, err := os.Lstat(manual); !os.IsNotExist(err) {
		t.Fatalf("the interrupted backup published %s: %v", manual, err)
	}
	if left := dotFiles(t, dest); len(left) != 0 {
		t.Fatalf("the interrupted backup left %v", left)
	}

	if err := os.RemoveAll(filepath.Join(dir, "files", "big")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	stale := []string{".atrium-20200101T030000Z.backup.123456", ".atrium-snapshot.234567", ".atrium-snapshot.234567-journal"}
	kept := []string{".atrium-snapshot.345678", ".atrium-notes.456789"}
	for _, name := range append(slices.Clone(stale), kept...) {
		path := filepath.Join(dest, name)
		if err := os.WriteFile(path, []byte("left by a killed run"), 0o600); err != nil {
			t.Fatal(err)
		}
		if name != kept[0] {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-1, backup.JobKey)
	if ran := runApp(t, "agent", "tick"); ran.code != cli.ExitOK {
		t.Fatalf("agent tick after the interrupted backup: exit %d %q", ran.code, ran.stderr)
	}
	if status, text := schedulerStatus(t, d, passrun.Backup); status != "ok" {
		t.Fatalf("backup job status %s (%s) after the interrupted backup, want ok", status, text)
	}
	if archives := backupArchives(t, dest); len(archives) != 1 {
		t.Fatalf("archives after the next run %v, want 1", archives)
	}
	if left := dotFiles(t, dest); !slices.Equal(left, []string{kept[1], kept[0]}) {
		t.Fatalf("temporary files after the next run %v, want only the recent and unrelated %v", left, kept)
	}
}
