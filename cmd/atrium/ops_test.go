package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/unit"
)

var opsMetricNames = []string{
	"atrium_budget_monthly_usd",
	"atrium_budget_spent_usd",
	"atrium_budget_spent_ratio",
	"atrium_scheduler_jobs",
	"atrium_scheduler_job_runs_total",
	"atrium_scheduler_job_next_due_timestamp_seconds",
	"atrium_scheduler_job_last_started_timestamp_seconds",
	"atrium_db_exclusive_waits_total",
	"atrium_db_exclusive_timeouts_total",
	"atrium_http_route_requests_total",
}

func opsAddr(t *testing.T) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ops-addr")
	setEnv(t, settingEnv(ops.ListenKey), "127.0.0.1:0")
	setEnv(t, "ATRIUM_OPS_ADDR_FILE", path)
	return func() string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("the ops listener did not report its address: %v", err)
		}
		return "http://" + strings.TrimSpace(string(raw))
	}
}

func readiness(t *testing.T, base string) (int, string) {
	t.Helper()
	status, body, _ := fetch(t, http.DefaultClient, "GET", base+"/readyz", nil)
	var got struct{ Status string }
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("readyz body %q: %v", body, err)
	}
	return status, got.Status
}

func TestOpsListenerServesMetricsAndReadinessApartFromThePublicListener(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept(unit.Notifications, "false"))
	opsBase := opsAddr(t)

	path := filepath.Join(dir, "atrium.db")
	if err := db.Migrate(context.Background(), mustConnect(t, path, db.Key{}), path); err != nil {
		t.Fatal(err)
	}
	held, err := db.Exclusive(context.Background(), path, 0)
	if err != nil {
		t.Fatal(err)
	}
	var timeout *db.LockTimeout
	if _, err := db.Exclusive(context.Background(), path, 20*time.Millisecond); !errors.As(err, &timeout) {
		t.Fatalf("contending lock: %v, want a timeout", err)
	}
	_ = held.Close()

	base := startServeIn(t, dir)

	status, body, _ := fetch(t, http.DefaultClient, "GET", opsBase()+"/metrics", nil)
	if status != http.StatusOK {
		t.Fatalf("ops /metrics = %d", status)
	}
	for _, name := range opsMetricNames {
		if !strings.Contains(body, "# TYPE "+name+" ") {
			t.Errorf("ops /metrics lacks %s", name)
		}
	}
	for _, line := range []string{"atrium_db_exclusive_waits_total ", "atrium_db_exclusive_timeouts_total "} {
		if strings.Contains(body, "\n"+line+"0\n") {
			t.Errorf("%s did not count the contended lock", strings.TrimSpace(line))
		}
	}
	for _, p := range []string{"/metrics", "/readyz", "/debug/pprof/"} {
		if status, _, _ := fetch(t, http.DefaultClient, "GET", base+p, nil); status != http.StatusNotFound {
			t.Errorf("public %s = %d, want 404", p, status)
		}
	}
	if status, _, _ := fetch(t, http.DefaultClient, "GET", base+"/healthz", nil); status != http.StatusOK {
		t.Errorf("public /healthz = %d", status)
	}
	if status, _, _ := fetch(t, http.DefaultClient, "GET", opsBase()+"/debug/pprof/", nil); status != http.StatusNotFound {
		t.Errorf("ops pprof = %d with pprof off, want 404", status)
	}
	if status, state := readiness(t, opsBase()); status != http.StatusOK || state != ops.StateReady {
		t.Errorf("readyz = %d %s, want 200 ready", status, state)
	}

	d := mustConnect(t, path, db.Key{})
	if _, err := d.Write.Exec("INSERT INTO schema_migrations (version, applied_at) VALUES (9999, 0)"); err != nil {
		t.Fatal(err)
	}
	if status, state := readiness(t, opsBase()); status != http.StatusServiceUnavailable || state != ops.StateNotReady {
		t.Errorf("readyz with a newer schema = %d %s, want 503 not_ready", status, state)
	}
}

func TestOpsPprofFollowsItsSetting(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(ops.PprofKey), "true")
	opsBase := opsAddr(t)
	startServeIn(t, dir)

	if status, _, _ := fetch(t, http.DefaultClient, "GET", opsBase()+"/debug/pprof/", nil); status != http.StatusOK {
		t.Errorf("ops pprof = %d with pprof on, want 200", status)
	}
}

func mustConnect(t *testing.T, path string, key db.Key) *db.DB {
	t.Helper()
	d, err := db.Connect(path, db.WithKey(key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
