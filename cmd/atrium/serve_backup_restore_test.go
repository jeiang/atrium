//go:build !modernc

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/cli"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/passrun"
)

func TestServeBackupJobArchiveIsSealedAndRestoresWithoutItsClaim(t *testing.T) {
	requireCipher(t)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	key := keyFor(t)
	if key.IsZero() {
		t.Fatal("the fixture database has no key")
	}
	const secretText = "board minutes: confidential"
	writeStateFiles(t, dir, map[string]string{"files/threads/1/minutes.txt": secretText})
	dest := t.TempDir()
	setEnv(t, "ATRIUM_HOST_CONFIG", backupHost(t, dir, "backup_directory = \""+dest+"\""))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	var lastRun *int64
	if err := d.Read.QueryRow("SELECT last_run_id FROM jobs WHERE declaration_key = ?", backup.JobKey).Scan(&lastRun); err != nil {
		t.Fatal(err)
	}
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-60, backup.JobKey)

	startServeIn(t, dir)
	waitUntil(t, 60*time.Second, "the scheduled backup run", schedulerTerminal(d, passrun.Backup))
	if status, text := schedulerStatus(t, d, passrun.Backup); status != "ok" {
		t.Fatalf("backup job status %s: %s", status, text)
	}
	archives := backupArchives(t, dest)
	if len(archives) != 1 {
		t.Fatalf("archives after the scheduled run %v, want 1", archives)
	}
	archive := archives[0]

	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	for name, needle := range map[string][]byte{"hex key": []byte(rightKeyHex), "raw key": key.Raw(), "file text": []byte(secretText)} {
		if bytes.Contains(raw, needle) {
			t.Errorf("the job archive contains the %s", name)
		}
	}
	entries := archiveEntries(t, archive)
	if bytes.HasPrefix(entries["atrium.db"], []byte("SQLite format 3\x00")) {
		t.Error("the job archived a plaintext database")
	}
	if header := string(entries["header.json"]); !strings.Contains(header, `"seal"`) || !strings.Contains(header, dbkey.ID(key)) {
		t.Errorf("job archive header %q, want a seal naming key id %s", header, dbkey.ID(key))
	}
	live := filepath.Join(dir, "atrium.db")
	if bad := runProcess(t, "restore --verify "+archive, "ATRIUM_DB="+live, "ATRIUM_DB_KEY=", "CREDENTIALS_DIRECTORY="); bad.code != cli.ExitError || !strings.Contains(bad.stderr, "no key is configured") {
		t.Errorf("verify of the job archive without a key exited %d, stderr %q", bad.code, bad.stderr)
	}

	fresh := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(fresh, 0o700); err != nil {
		t.Fatal(err)
	}
	freshDB := filepath.Join(fresh, "atrium.db")
	env := []string{"ATRIUM_DB=" + freshDB, "ATRIUM_DB_KEY=" + rightKeyHex, "ATRIUM_CACHE_DIR=" + filepath.Join(fresh, "cache")}
	if res := runProcess(t, "restore "+archive, env...); res.code != cli.ExitOK || !strings.Contains(res.stdout, "restored "+freshDB) {
		t.Fatalf("restore of the job archive exited %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}
	if body, err := os.ReadFile(filepath.Join(fresh, "files", "threads", "1", "minutes.txt")); err != nil || string(body) != secretText {
		t.Fatalf("restored file %q, %v", body, err)
	}

	restored := mustConnect(t, freshDB, key)
	if n := asstCount(t, restored, "SELECT COUNT(*) FROM job_runs WHERE status = 'running'"); n != 0 {
		t.Errorf("%d running job runs in the restored database, want 0", n)
	}
	if n := asstCount(t, restored, "SELECT COUNT(*) FROM job_resource_leases"); n != 0 {
		t.Errorf("%d resource leases in the restored database, want 0", n)
	}
	if n := asstCount(t, restored, "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = ?", backup.JobKey); n != 0 {
		t.Errorf("%d backup job runs in the restored database, want 0", n)
	}
	var owner *string
	var restoredLast *int64
	var due *float64
	if err := restored.Read.QueryRow("SELECT lease_owner, last_run_id, next_due FROM jobs WHERE declaration_key = ?", backup.JobKey).Scan(&owner, &restoredLast, &due); err != nil {
		t.Fatal(err)
	}
	if owner != nil {
		t.Errorf("restored backup job lease owner %q, want none", *owner)
	}
	if (restoredLast == nil) != (lastRun == nil) || (lastRun != nil && *restoredLast != *lastRun) {
		t.Errorf("restored backup job last run %v, want the run before the backup %v", restoredLast, lastRun)
	}
	if due == nil || *due <= float64(time.Now().Unix()) {
		t.Errorf("restored backup job next due %v, want the next occurrence", due)
	}
	_ = restored.Close()

	again := filepath.Join(t.TempDir(), "again.backup")
	if got := runProcess(t, "backup "+again, env...); got.code != cli.ExitOK || !strings.Contains(got.stdout, "backup written to "+again) || !strings.Contains(got.stdout, "encrypted, key id "+dbkey.ID(key)) {
		t.Fatalf("backup in the restored state exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
}
