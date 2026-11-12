//go:build !modernc

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
)

const inUse = "is in use by a running atrium serve or destructive database command"

func lockedServeEnv(t *testing.T, dir, keyHex string) string {
	t.Helper()
	live := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", live)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_DB_KEY", keyHex)
	setEnv(t, "ATRIUM_CONFIG", "")
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	return live
}

func startLockedServe(t *testing.T, dir string) *serveProcess {
	t.Helper()
	addrFile := filepath.Join(dir, "serve-addr")
	_ = os.Remove(addrFile)
	cmd := cliExitProcessArgs(t, []string{"serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local"})
	cmd.Env = append(envWithout(cmd.Env, "ATRIUM_SERVE_ADDR_FILE="), "ATRIUM_SERVE_ADDR_FILE="+addrFile)
	return startServeProcess(t, cmd, addrFile)
}

func refusedServe(t *testing.T, workDir, dbPath string, extra ...string) cliRun {
	t.Helper()
	scratch := t.TempDir()
	addrFile := filepath.Join(scratch, "serve-addr")
	cache := filepath.Join(scratch, "cache")
	cmd := cliExitProcessArgs(t, []string{"serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local"})
	cmd.Dir = workDir
	cmd.Env = append(envWithout(envWithout(envWithout(cmd.Env, "ATRIUM_SERVE_ADDR_FILE="), "ATRIUM_DB="), "ATRIUM_CACHE_DIR="),
		append([]string{"ATRIUM_SERVE_ADDR_FILE=" + addrFile, "ATRIUM_DB=" + dbPath, "ATRIUM_CACHE_DIR=" + cache}, extra...)...)
	var stdout, stderr lockedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	got := runBounded(t, cmd)
	run := cliRun{got, stdout.String(), stderr.String()}
	if _, err := os.Stat(addrFile); err == nil {
		t.Errorf("serve on %s reported a listening address although it was refused; stderr %q", dbPath, run.stderr)
	}
	if _, err := os.Stat(cache); err == nil {
		t.Errorf("serve on %s created its cache directory although it was refused", dbPath)
	}
	return run
}

func runBounded(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return exitCode(err)
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("%v did not exit", cmd.Args)
		return 0
	}
}

func dbFileState(t *testing.T, path string) string {
	t.Helper()
	var parts []string
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			parts = append(parts, suffix+"="+fileDigest(t, path+suffix))
		} else {
			parts = append(parts, suffix+"=absent")
		}
	}
	return strings.Join(parts, " ")
}

func TestServeExcludesASecondServeOnTheSameDatabase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	live := lockedServeEnv(t, dir, rightKeyHex)
	serve := startLockedServe(t, dir)

	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ workDir, path string }{
		"same path":     {dir, live},
		"symlinked dir": {dir, filepath.Join(alias, "atrium.db")},
		"relative path": {dir, "atrium.db"},
	} {
		got := refusedServe(t, tc.workDir, tc.path)
		if got.code == cli.ExitOK || !strings.Contains(got.stderr, inUse) {
			t.Errorf("%s: second serve exited %d, stderr %q; want nonzero with %q", name, got.code, got.stderr, inUse)
		}
	}
	if code := serve.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Fatalf("first serve exited %d; stderr %q", code, serve.stderr.String())
	}

	link := live + ".alt"
	if err := os.Link(live, link); err != nil {
		t.Fatal(err)
	}
	before := dbFileState(t, live)
	if got := refusedServe(t, dir, live); got.code == cli.ExitOK || !strings.Contains(got.stderr, "hard links") {
		t.Errorf("serve on a hard-linked database exited %d, stderr %q; want nonzero with a hard link refusal", got.code, got.stderr)
	}
	if after := dbFileState(t, live); after != before {
		t.Errorf("the refused serve changed the database files: %s -> %s", before, after)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}

	crashed := startLockedServe(t, dir)
	if err := crashed.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = crashed.wait()
	again := startLockedServe(t, dir)
	if code := again.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Errorf("serve after a crashed serve exited %d; stderr %q", code, again.stderr.String())
	}
	if _, err := os.Stat(live + ".serve.lock"); err != nil {
		t.Errorf("the serve lock file was removed: %v", err)
	}
}

func TestServeRefusesDestructiveMaintenanceAndKeepsBackupOnline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	live := lockedServeEnv(t, dir, rightKeyHex)
	serve := startLockedServe(t, dir)

	copied := filepath.Join(t.TempDir(), "copy.db")
	if got := runProcess(t, "db backup "+copied); got.code != cli.ExitOK {
		t.Fatalf("db backup during serve exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	archive := filepath.Join(t.TempDir(), "state.tar.zst")
	if got := runProcess(t, "backup "+archive); got.code != cli.ExitOK {
		t.Fatalf("archive backup during serve exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	if got := runProcess(t, "db check"); got.code != cli.ExitOK || !strings.Contains(got.stdout, "integrity_check: ok") {
		t.Errorf("db check during serve exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	if got := runProcess(t, "restore --verify "+archive); got.code != cli.ExitOK {
		t.Errorf("restore --verify during serve exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}

	next := writeKey(t, t.TempDir(), "next.key", otherKeyHex)
	source := copyV2Fixture(t, t.TempDir())
	refusals := map[string]cliRun{
		"rekey":     runProcess(t, "db rekey --new-key-file "+next),
		"encrypt":   runProcess(t, "db encrypt"),
		"import-v2": runProcess(t, "db import-v2 "+source),
		"restore":   runProcess(t, "restore "+archive),
		"shell":     runProcessStdin(t, "CREATE TABLE shell_probe (a);\n", "db shell"),
	}
	for name, got := range refusals {
		if got.code != cli.ExitError || !strings.Contains(got.stderr, inUse) || strings.Contains(got.stdout+got.stderr, otherKeyHex) {
			t.Errorf("%s during serve exited %d, stdout %q, stderr %q; want a refusal naming the running serve", name, got.code, got.stdout, got.stderr)
		}
	}
	if code := serve.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Fatalf("serve exited %d; stderr %q", code, serve.stderr.String())
	}

	backup := mustConnect(t, copied, keyFor(t))
	var integrity string
	if err := backup.Read.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Errorf("the online backup integrity_check = %q, %v", integrity, err)
	}
	var copiedVersions, liveVersions int
	if err := backup.Read.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&copiedVersions); err != nil {
		t.Fatal(err)
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}
	restoredCopy := filepath.Join(t.TempDir(), "restored.db")
	copyFile(t, copied, restoredCopy)
	restored := mustConnect(t, restoredCopy, keyFor(t))
	if err := restored.Read.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Errorf("the restored online backup integrity_check = %q, %v", integrity, err)
	}
	if err := restored.Read.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&liveVersions); err != nil || liveVersions != copiedVersions || liveVersions == 0 {
		t.Errorf("the restored online backup holds %d migrations, want %d: %v", liveVersions, copiedVersions, err)
	}

	if got := runProcessStdin(t, "CREATE TABLE shell_probe (a);\nSELECT count(*) FROM shell_probe;\n", "db shell"); got.code != cli.ExitOK || got.stdout != "0\n" {
		t.Errorf("db shell after serve exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	if got := runProcess(t, "restore "+archive); got.code != cli.ExitOK || !strings.Contains(got.stdout, "restored "+live) {
		t.Errorf("restore after serve exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	if got := runProcess(t, "db rekey --new-key-file "+next); got.code != cli.ExitOK {
		t.Errorf("db rekey after serve exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(live + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if got := runProcess(t, "db import-v2 "+source, "ATRIUM_DB_KEY="+otherKeyHex); got.code != cli.ExitOK {
		t.Errorf("db import-v2 after serve exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
}

func TestServeRefusesEncryptionUntilItStops(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lockedServeEnv(t, dir, "")
	serve := startLockedServe(t, dir)
	if got := runProcess(t, "db encrypt", "ATRIUM_DB_KEY="+rightKeyHex); got.code != cli.ExitError || !strings.Contains(got.stderr, inUse) {
		t.Errorf("db encrypt during serve exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	if code := serve.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Fatalf("serve exited %d; stderr %q", code, serve.stderr.String())
	}
	if got := runProcess(t, "db encrypt", "ATRIUM_DB_KEY="+rightKeyHex); got.code != cli.ExitOK {
		t.Errorf("db encrypt after serve exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
}
