//go:build !modernc

package main

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func shellEnv(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB_KEY", "")
	setEnv(t, "ATRIUM_CONFIG", "")
	setEnv(t, "CREDENTIALS_DIRECTORY", "")
	return path, []string{"ATRIUM_DB=" + path, "ATRIUM_CACHE_DIR=" + filepath.Join(dir, "cache")}
}

func TestDatabaseShellKeepsCaseEndTriggers(t *testing.T) {
	t.Parallel()
	path, env := shellEnv(t)
	if got := runProcess(t, "settings set tz UTC", env...); got.code != cli.ExitOK {
		t.Fatalf("create database: exit %d stderr %q", got.code, got.stderr)
	}
	script := strings.Join([]string{
		"CREATE TABLE t (a INTEGER);",
		"CREATE TRIGGER t_trig AFTER UPDATE ON t BEGIN UPDATE t SET a = CASE WHEN NEW.a > 0 THEN NEW.a ELSE 0 END; END;",
		"INSERT INTO t (a) VALUES (1);",
		"UPDATE t SET a = 2;",
		"SELECT a FROM t;",
		"",
	}, "\n")
	got := runProcessStdin(t, script, "db shell", env...)
	if got.code != cli.ExitOK || strings.TrimSpace(got.stdout) != "2" {
		t.Fatalf("db shell: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRefusesDestinationUnderFiles(t *testing.T) {
	t.Parallel()
	path, env := shellEnv(t)
	if got := runProcess(t, "settings set tz UTC", env...); got.code != cli.ExitOK {
		t.Fatalf("create database: exit %d stderr %q", got.code, got.stderr)
	}
	files := filepath.Join(filepath.Dir(path), "files")
	if err := os.MkdirAll(files, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(files, "atrium.backup")
	got := runProcess(t, "backup "+dest, env...)
	if got.code == cli.ExitOK || !strings.Contains(got.stderr, "inside") {
		t.Fatalf("backup into files/: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("the refused backup wrote the destination")
	}
}

func TestRestoreRefusesV1Archive(t *testing.T) {
	t.Parallel()
	_, env := shellEnv(t)
	archive := filepath.Join(t.TempDir(), "v1.backup")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	header := []byte(`{"format":1,"created_at":"2026-01-01T00:00:00Z","schema":0,"files":0,"stored_bytes":0}`)
	if err := tw.WriteHeader(&tar.Header{Name: "header.json", Mode: 0o600, Size: int64(len(header)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(header); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	got := runProcess(t, "restore --verify "+archive, env...)
	if got.code == cli.ExitOK || !strings.Contains(got.stderr, "v1 archive") || !strings.Contains(got.stderr, "import-v1") {
		t.Fatalf("restore of a v1 archive: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
}

func TestRestoreKeepsNestedFilesDirectoryModes(t *testing.T) {
	t.Parallel()
	path, env := shellEnv(t)
	if got := runProcess(t, "settings set tz UTC", env...); got.code != cli.ExitOK {
		t.Fatalf("create database: exit %d stderr %q", got.code, got.stderr)
	}
	state := filepath.Dir(path)
	private := filepath.Join(state, "files", "private")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "secret.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(private, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "atrium.backup")
	if got := runProcess(t, "backup "+archive, env...); got.code != cli.ExitOK {
		t.Fatalf("backup: exit %d stderr %q", got.code, got.stderr)
	}
	fresh := filepath.Join(t.TempDir(), "fresh", "atrium.db")
	if got := runProcess(t, "restore "+archive, "ATRIUM_DB="+fresh, "ATRIUM_CACHE_DIR="+filepath.Join(filepath.Dir(fresh), "cache")); got.code != cli.ExitOK {
		t.Fatalf("restore: exit %d stderr %q", got.code, got.stderr)
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(fresh), "files", "private"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("restored private directory mode = %o, want 700", info.Mode().Perm())
	}
}
