package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
)

func backupHost(t *testing.T, dir string, core ...string) string {
	t.Helper()
	path := accountsFile(t, dir)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(core) > 0 {
		body = append(body, []byte("\n[core]\n"+strings.Join(core, "\n")+"\n")...)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func backupArchives(t *testing.T, dir string) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(dir, "atrium-*.backup"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(found)
	return found
}

func oldArchives(t *testing.T, dir string, names ...string) {
	t.Helper()
	for i, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("old archive"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(-time.Duration(30+i) * 24 * time.Hour)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
}

func backupJobRow(t *testing.T, d *db.DB) (string, *float64) {
	t.Helper()
	var schedule string
	var due *float64
	if err := d.Read.QueryRow("SELECT schedule, next_due FROM jobs WHERE declaration_key = ?", backup.JobKey).Scan(&schedule, &due); err != nil {
		t.Fatal(err)
	}
	return schedule, due
}

func TestServeBackupJobArchivesAndPrunes(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	dest := t.TempDir()
	oldArchives(t, dest, "atrium-20200103T030000Z.backup", "atrium-20200102T030000Z.backup", "atrium-20200101T030000Z.backup")
	unrelated := filepath.Join(dest, "pre-migration.backup")
	if err := os.WriteFile(unrelated, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(unrelated, stale, stale); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", backupHost(t, dir, "backup_directory = \""+dest+"\"", "backup_keep = 2", "backup_keep_days = 0"))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Add(-72*time.Hour).Unix()), backup.JobKey)

	startServeIn(t, dir)
	waitUntil(t, 60*time.Second, "the missed backup run at serve start", schedulerTerminal(d, passrun.Backup))
	if status, text := schedulerStatus(t, d, passrun.Backup); status != "ok" {
		t.Fatalf("backup job status %s: %s", status, text)
	}
	time.Sleep(3 * time.Second)
	if n := schedulerRuns(t, d, passrun.Backup); n != 1 {
		t.Fatalf("%d backup job runs after three missed days, want 1", n)
	}
	if ok, failed, _ := passRows(t, dir, passrun.Backup); ok != 1 || failed != 0 {
		t.Fatalf("backup pass rows ok=%d failed=%d, want 1 and 0", ok, failed)
	}
	archives := backupArchives(t, dest)
	want := []string{filepath.Join(dest, "atrium-20200103T030000Z.backup")}
	if len(archives) != 2 || archives[0] != want[0] || !strings.HasPrefix(filepath.Base(archives[1]), "atrium-"+time.Now().UTC().Format("20060102")) {
		t.Fatalf("archives after the run %v, want the new archive and the newest old one %v", archives, want)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated backup file: %v", err)
	}
	if got := runApp(t, "restore", "--verify", archives[1]); got.code != cli.ExitOK || !strings.Contains(got.stdout, "integrity_check: ok") {
		t.Fatalf("restore --verify of the job archive exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	if _, due := backupJobRow(t, d); due == nil || *due <= float64(time.Now().Unix()) || *due > float64(time.Now().Add(25*time.Hour).Unix()) {
		t.Fatalf("backup job next due %v after the run, want the next daily occurrence", due)
	}
}

func TestBackupJobTakesItsScheduleFromTheHost(t *testing.T) {
	zone, err := config.Zone(config.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		value, schedule string
		hour            int
		warned          bool
	}{
		"valid":   {"weekly mon 05:15", "weekly mon 05:15", 5, false},
		"invalid": {fmt.Sprintf("daily 25:%d", 60+time.Now().Nanosecond()%40), backup.DefaultSchedule, 3, true},
		"absent":  {"", backup.DefaultSchedule, 3, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir, _, _ := moduleFixture(t, flagsExcept("", ""))
			var core []string
			if tc.value != "" {
				core = append(core, "backup_schedule = \""+tc.value+"\"")
			}
			setEnv(t, "ATRIUM_HOST_CONFIG", backupHost(t, dir, core...))
			d := asstDB(t, dir)
			ran := runAppLogged(t, "agent", "tick")
			if ran.code != cli.ExitOK {
				t.Fatalf("agent tick exit %d: %s", ran.code, ran.stderr)
			}
			schedule, due := backupJobRow(t, d)
			if schedule != tc.schedule || due == nil {
				t.Fatalf("backup job schedule %q next due %v, want %q", schedule, due, tc.schedule)
			}
			if at := time.Unix(int64(*due), 0).In(zone); at.Hour() != tc.hour {
				t.Fatalf("backup job next due %s, want hour %d in %s", at, tc.hour, zone)
			}
			warned := strings.Contains(ran.stderr, "host schedule "+backup.KeySchedule+" not applied")
			if warned != tc.warned {
				t.Fatalf("host schedule warning %v, want %v; stderr %q", warned, tc.warned, ran.stderr)
			}
			if n := schedulerRuns(t, d, passrun.Backup); n != 0 {
				t.Fatalf("%d backup runs at declaration, want 0", n)
			}
			again := runAppLogged(t, "agent", "tick")
			if strings.Contains(again.stderr, "host schedule "+backup.KeySchedule+" not applied") {
				t.Fatalf("second tick repeated the host schedule warning; stderr %q", again.stderr)
			}
		})
	}
}

func TestBackupCommandSharesTheJobClaim(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	dest := t.TempDir()
	setEnv(t, "ATRIUM_HOST_CONFIG", backupHost(t, dir, "backup_directory = \""+dest+"\""))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)

	release := make(chan struct{})
	held := make(chan error, 1)
	go func() {
		_, err := scheduler.RunNow(context.Background(), d, filepath.Join(dir, "atrium.db"), "UTC", backup.Job(nil, nil), 0, func(context.Context, scheduler.Run) (scheduler.Result, error) {
			<-release
			return scheduler.Result{}, nil
		})
		held <- err
	}()
	waitUntil(t, 30*time.Second, "the backup job claim", func() bool { return schedulerRuns(t, d, passrun.Backup) == 1 })

	manual := filepath.Join(t.TempDir(), "manual.backup")
	got := runApp(t, "backup", manual)
	if got.code != cli.ExitError || !strings.Contains(got.stderr, "held by another invocation") {
		t.Fatalf("backup beside the running job = (%d, %q), want exit %d and a held-lock refusal", got.code, got.stderr, cli.ExitError)
	}
	if _, err := os.Lstat(manual); !os.IsNotExist(err) {
		t.Fatalf("refused backup left %s: %v", manual, err)
	}
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-1, backup.JobKey)
	if ran := runApp(t, "agent", "tick"); ran.code != cli.ExitOK && ran.code != cli.ExitTempFail {
		t.Fatalf("agent tick beside the running job: exit %d: %s", ran.code, ran.stderr)
	}
	if n := schedulerRuns(t, d, passrun.Backup); n != 1 || len(backupArchives(t, dest)) != 0 {
		t.Fatalf("%d backup runs and archives %v beside the running job, want 1 and none", n, backupArchives(t, dest))
	}

	close(release)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	got = runApp(t, "backup", manual)
	if got.code != cli.ExitOK || !strings.Contains(got.stdout, "backup written to "+manual) {
		t.Fatalf("backup after the job = (%d, %q, %q)", got.code, got.stdout, got.stderr)
	}
	if ok, _, _ := passRows(t, dir, passrun.Backup); ok != 1 {
		t.Fatalf("backup pass rows ok=%d after the manual run, want 1", ok)
	}
}

func TestBackupJobSkipsWithoutADirectoryAndPrunesNothingOnFailure(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", backupHost(t, dir))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-1, backup.JobKey)
	if ran := runApp(t, "agent", "tick"); ran.code != cli.ExitOK {
		t.Fatalf("agent tick without a backup directory: exit %d %q", ran.code, ran.stderr)
	}
	if status, _ := schedulerStatus(t, d, passrun.Backup); status != "skipped_unconfigured" {
		t.Fatalf("backup job status %s without a directory, want skipped_unconfigured", status)
	}
	if ok, failed, _ := passRows(t, dir, passrun.Backup); ok != 0 || failed != 0 {
		t.Fatalf("backup pass rows ok=%d failed=%d without a directory, want none", ok, failed)
	}
	for range 2 {
		asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-1, backup.JobKey)
		if ran := runApp(t, "agent", "tick"); ran.code != cli.ExitOK {
			t.Fatalf("repeated agent tick without a backup directory: exit %d %q", ran.code, ran.stderr)
		}
	}
	if n := schedulerRuns(t, d, passrun.Backup); n != 1 {
		t.Fatalf("%d backup job rows after three unconfigured runs, want one merged row", n)
	}

	if os.Geteuid() == 0 {
		return
	}
	dest := t.TempDir()
	oldArchives(t, dest, "atrium-20200102T030000Z.backup", "atrium-20200101T030000Z.backup")
	if err := os.Chmod(dest, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dest, 0o700) })
	setEnv(t, "ATRIUM_HOST_CONFIG", backupHost(t, dir, "backup_directory = \""+dest+"\"", "backup_keep = 1", "backup_keep_days = 0"))
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-1, backup.JobKey)
	runApp(t, "agent", "tick")
	if status, text := schedulerStatus(t, d, passrun.Backup); status != "error" {
		t.Fatalf("backup job status %s (%s) into a read-only directory, want error", status, text)
	}
	if _, failed, _ := passRows(t, dir, passrun.Backup); failed != 1 {
		t.Fatalf("backup failed pass rows %d, want 1", failed)
	}
	if archives := backupArchives(t, dest); len(archives) != 2 {
		t.Fatalf("archives after a failed run %v, want both old archives kept", archives)
	}
}
