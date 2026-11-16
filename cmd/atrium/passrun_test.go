//go:build !modernc

package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/passrun"
)

func legacyPassRows(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow("SELECT (SELECT COUNT(*) FROM pass_runs) + (SELECT COUNT(*) FROM pass_run_alerts)").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPassesRecordOutcomesInJobRuns(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if got := runApp(t, "settings", "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", got.code, got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	old := float64(time.Now().Add(-91 * 24 * time.Hour).Unix())
	if _, err := d.Write.Exec("INSERT INTO pass_runs (job, started_at, finished_at, outcome, message) VALUES ('scan', ?, ?, 'failed', 'legacy')", old, old); err != nil {
		t.Fatal(err)
	}

	if got := runApp(t, "proposals", "scan-all"); got.code != cli.ExitOK {
		t.Fatalf("scan-all: %d %s", got.code, got.stderr)
	}
	if ok, failed, _ := passRows(t, dir, passrun.Scan); ok != 1 || failed != 0 {
		t.Errorf("scan runs ok=%d failed=%d, want the new success only", ok, failed)
	}

	missing := filepath.Join(dir, "no-such-dir", "out.backup")
	if got := runApp(t, "backup", missing); got.code == cli.ExitOK {
		t.Fatal("backup into a missing directory succeeded")
	}
	if ok, failed, msg := passRows(t, dir, passrun.Backup); ok != 0 || failed != 1 || !strings.Contains(msg, "no-such-dir") {
		t.Errorf("backup runs ok=%d failed=%d message %q, want one failure naming the path", ok, failed, msg)
	}
	if got := runApp(t, "backup", filepath.Join(dir, "out.backup")); got.code != cli.ExitOK {
		t.Fatalf("backup: %d %s", got.code, got.stderr)
	}
	if ok, failed, _ := passRows(t, dir, passrun.Backup); ok != 1 || failed != 1 {
		t.Errorf("backup runs ok=%d failed=%d after a success", ok, failed)
	}
	if n := legacyPassRows(t, d); n != 1 {
		t.Errorf("legacy pass rows %d, want only the seeded row", n)
	}
}

func TestLegacyPassRowsLeavePassesReadyOnFirstStart(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if got := runApp(t, "settings", "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", got.code, got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	old := float64(time.Now().Add(-30 * 24 * time.Hour).Unix())
	for _, job := range []string{passrun.Backup, passrun.Sync, passrun.CalDAV} {
		if _, err := d.Write.Exec("INSERT INTO pass_runs (job, started_at, finished_at, outcome, message) VALUES (?, ?, ?, 'failed', 'legacy')", job, old, old); err != nil {
			t.Fatal(err)
		}
	}
	opsBase := opsAddr(t)
	startServeIn(t, dir)
	if status, state := readiness(t, opsBase); status != http.StatusOK || state != ops.StateReady {
		t.Errorf("readyz = %d %s with only legacy pass rows, want ready", status, state)
	}
	_, body, _ := fetch(t, http.DefaultClient, "GET", opsBase+"/metrics", nil)
	if strings.Contains(body, "atrium_job_stale{") {
		t.Errorf("metrics report a pass from legacy rows:\n%s", body)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM notifications"); n != 0 {
		t.Errorf("%d notifications from legacy pass rows, want none", n)
	}
}

func TestFailedPassIsStaleOnMetricsAndReadinessAndNoticedOnce(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	push, browser := pushRig(t, dir)
	count := push.count
	if got := runApp(t, "settings", "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", got.code, got.stderr)
	}
	browser.store(t, mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{}))
	if got := runApp(t, "backup", filepath.Join(dir, "missing", "x.backup")); got.code == cli.ExitOK {
		t.Fatal("failing backup succeeded")
	}

	t.Run("stale after a failure", func(t *testing.T) {
		opsBase := opsAddr(t)
		startServeIn(t, dir)
		_, body, _ := fetch(t, http.DefaultClient, "GET", opsBase+"/metrics", nil)
		if !strings.Contains(body, `atrium_job_stale{pass="backup"} 1`) {
			t.Errorf("metrics lack the stale backup gauge:\n%s", body)
		}
		if status, state := readiness(t, opsBase); status != http.StatusOK || state != ops.StateDegraded {
			t.Errorf("readyz = %d %s, want 200 degraded", status, state)
		}
		deadline := time.Now().Add(5 * time.Second)
		for count() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if count() != 1 {
			t.Fatalf("%d notices, want one stale notice for backup", count())
		}
		if notice := browser.decrypt(t, push.wait(t, 1).body); !strings.Contains(fmt.Sprint(notice["title"]), "backup is stale") {
			t.Fatalf("notice %v, want the backup stale notice", notice)
		}
	})

	t.Run("not repeated on the next start", func(t *testing.T) {
		opsAddr(t)
		startServeIn(t, dir)
		time.Sleep(300 * time.Millisecond)
		if count() != 1 {
			t.Errorf("notices %d after a restart, want still 1", count())
		}
	})

	if got := runApp(t, "backup", filepath.Join(dir, "ok.backup")); got.code != cli.ExitOK {
		t.Fatalf("backup: %d %s", got.code, got.stderr)
	}

	t.Run("ready after a success", func(t *testing.T) {
		opsBase := opsAddr(t)
		startServeIn(t, dir)
		if status, state := readiness(t, opsBase); status != http.StatusOK || state != ops.StateReady {
			t.Errorf("readyz = %d %s after a success, want ready", status, state)
		}
		_, body, _ := fetch(t, http.DefaultClient, "GET", opsBase+"/metrics", nil)
		if !strings.Contains(body, `atrium_job_last_success_timestamp_seconds{pass="backup"}`) {
			t.Errorf("metrics lack the last-success gauge:\n%s", body)
		}
		time.Sleep(300 * time.Millisecond)
		if count() != 1 {
			t.Errorf("notices %d after recovery, want still 1", count())
		}
	})
}

func TestDisabledNotificationsWatchNoStalePasses(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept(module.Notifications, "false"))
	push, browser := pushRig(t, dir)
	if got := runApp(t, "settings", "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", got.code, got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	browser.store(t, d)
	if got := runApp(t, "backup", filepath.Join(dir, "missing", "x.backup")); got.code == cli.ExitOK {
		t.Fatal("failing backup succeeded")
	}
	rows := func(table string) int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	opsAddr(t)
	startServeIn(t, dir)
	time.Sleep(time.Second)
	if n := legacyPassRows(t, d); n != 0 {
		t.Errorf("%d legacy pass rows written while notifications are off", n)
	}
	if n := rows("notifications"); n != 0 {
		t.Errorf("%d notifications recorded while notifications are off", n)
	}
	if n := push.count(); n != 0 {
		t.Fatalf("%d pushes sent while notifications are off", n)
	}
	if got := runApp(t, "settings", "set", module.FlagKey(module.Notifications), "true"); got.code != cli.ExitOK {
		t.Fatalf("notifications_enabled true: exit %d stderr %q", got.code, got.stderr)
	}
	if notice := browser.decrypt(t, push.wait(t, 1).body); !strings.Contains(fmt.Sprint(notice["title"]), "backup is stale") {
		t.Fatalf("notice %v after enabling notifications, want the backup stale notice", notice)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM notifications WHERE title LIKE '%backup is stale%'"); n != 1 {
		t.Errorf("%d stale notices after enabling notifications, want 1", n)
	}
	if n := legacyPassRows(t, d); n != 0 {
		t.Errorf("%d legacy pass rows after enabling notifications, want none", n)
	}
}
