package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

const lockHolderEnv = "ATRIUM_LOCK_TIMEOUT_TEST_HOLDER"

func TestMain(m *testing.M) {
	if path := os.Getenv(lockHolderEnv); path != "" {
		lock, err := db.Exclusive(context.Background(), path, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer func() { _ = lock.Close() }()
		fmt.Println("locked")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestMoneyRefreshReportsExclusiveLockTimeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background(), d, path); err != nil {
		_ = d.Close()
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), lockHolderEnv+"="+path)
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var childStderr bytes.Buffer
	cmd.Stderr = &childStderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("lock holder output = %q, %v; stderr %q", line, err, childStderr.String())
	}

	var out, stderr bytes.Buffer
	code := newApp().Run(context.Background(), []string{"money", "refresh"}, &out, &stderr)
	if code == cli.ExitOK || !strings.Contains(stderr.String(), "timed out after") || !strings.Contains(stderr.String(), path+".lock") {
		t.Fatalf("money refresh = (%d, stdout %q, stderr %q), want non-zero and a clear lock timeout", code, out.String(), stderr.String())
	}
}
