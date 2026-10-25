//go:build !modernc

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/passrun"
)

func passRows(t *testing.T, dir, job string) (ok, failed int, message string) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if err := d.Read.QueryRow("SELECT COUNT(*) FILTER (WHERE outcome = 'ok'), COUNT(*) FILTER (WHERE outcome = 'failed'), "+
		"COALESCE((SELECT message FROM pass_runs WHERE job = ? ORDER BY id DESC LIMIT 1), '') FROM pass_runs WHERE job = ?", job, job).Scan(&ok, &failed, &message); err != nil {
		t.Fatal(err)
	}
	return ok, failed, message
}

func TestPassesRecordOutcomesAndPruneByRetention(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	if got := runApp("settings", "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", got.code, got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	old := float64(time.Now().Add(-48 * time.Hour).Unix())
	if _, err := d.Write.Exec("INSERT INTO pass_runs (job, started_at, finished_at, outcome) VALUES ('scan', ?, ?, 'ok')", old, old); err != nil {
		t.Fatal(err)
	}
	t.Setenv(settingEnv(passrun.RetentionKey), "1")

	if got := runApp("proposals", "scan-all"); got.code != cli.ExitOK {
		t.Fatalf("scan-all: %d %s", got.code, got.stderr)
	}
	if ok, failed, _ := passRows(t, dir, passrun.Scan); ok != 1 || failed != 0 {
		t.Errorf("scan rows ok=%d failed=%d, want the new success only (old row pruned)", ok, failed)
	}

	missing := filepath.Join(dir, "no-such-dir", "out.backup")
	if got := runApp("backup", missing); got.code == cli.ExitOK {
		t.Fatal("backup into a missing directory succeeded")
	}
	if ok, failed, msg := passRows(t, dir, passrun.Backup); ok != 0 || failed != 1 || !strings.Contains(msg, "no-such-dir") {
		t.Errorf("backup rows ok=%d failed=%d message %q, want one failure naming the path", ok, failed, msg)
	}
	if got := runApp("backup", filepath.Join(dir, "out.backup")); got.code != cli.ExitOK {
		t.Fatalf("backup: %d %s", got.code, got.stderr)
	}
	if ok, failed, _ := passRows(t, dir, passrun.Backup); ok != 1 || failed != 1 {
		t.Errorf("backup rows ok=%d failed=%d after a success", ok, failed)
	}
}

func TestFailedPassIsStaleOnMetricsAndReadinessAndNoticedOnce(t *testing.T) {
	var mu sync.Mutex
	var notices []string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			notices = append(notices, string(raw))
			mu.Unlock()
		}
	}))
	t.Cleanup(ntfy.Close)
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(notices)
	}
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	t.Setenv(settingEnv(notify.KeyURL), ntfy.URL)
	if got := runApp("settings", "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", got.code, got.stderr)
	}
	if got := runApp("backup", filepath.Join(dir, "missing", "x.backup")); got.code == cli.ExitOK {
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
		if count() != 1 || !strings.Contains(notices[0], "backup is stale") {
			t.Fatalf("notices %v, want one stale notice for backup", notices)
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

	if got := runApp("backup", filepath.Join(dir, "ok.backup")); got.code != cli.ExitOK {
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
