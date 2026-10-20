package run

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

const lockTestPathEnv = "ATRIUM_RUN_LOCK_TEST_PATH"

func TestMain(m *testing.M) {
	if path := os.Getenv(lockTestPathEnv); path != "" {
		os.Exit(lockTestHolder(path))
	}
	os.Exit(m.Run())
}

func lockTestHolder(path string) int {
	lock, err := db.Exclusive(context.Background(), path, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("locked")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	if err := lock.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func TestPassWaitsForExternalLockAndRunsAfterRelease(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), lockTestPathEnv+"="+w.cfg.DBPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("lock holder output = %q, %v", line, err)
	}

	done := make(chan error, 1)
	go func() { done <- w.pass.Locked(ctx, []string{config.ICloud}) }()
	select {
	case err := <-done:
		t.Fatalf("sync pass finished while another process held the lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if len(w.opens()) != 0 {
		t.Fatal("sync pass opened an account while another process held the lock")
	}
	if _, err := io.WriteString(stdin, "\n"); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("sync pass did not proceed after the lock was released")
	}
	if len(w.opens()) == 0 {
		t.Fatal("sync pass did not open an account after lock release")
	}
}
