package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/serverops"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

func fixtureEnv(f *fixture) config.Env {
	return func(name string) (string, bool) {
		value, ok := f.env[name]
		return value, ok
	}
}

func samplerFinished(t *testing.T, d *db.DB) func() bool {
	t.Helper()
	var before int64
	if err := d.Read.QueryRow("SELECT COALESCE(MAX(id), 0) FROM job_runs").Scan(&before); err != nil {
		t.Fatal(err)
	}
	return func() bool {
		var count int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM job_runs WHERE id > ? AND job_id = (SELECT id FROM jobs WHERE name = 'ops-sampler') AND ended_at IS NOT NULL", before).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count > 0
	}
}

func samplesAtLeast(t *testing.T, d *db.DB, n int) func() bool {
	t.Helper()
	return func() bool {
		var count int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM ops_samples").Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count >= n
	}
}

func waitForOps(t *testing.T, d *db.DB, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(180 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			var state, runErr string
			_ = d.Read.QueryRow("SELECT r.state, COALESCE(r.error, '') FROM runs r JOIN threads th ON th.id = r.thread_id WHERE th.kind = 'worker' ORDER BY r.id DESC LIMIT 1").Scan(&state, &runErr)
			var buf [1 << 20]byte
			n := runtime.Stack(buf[:], true)
			t.Fatalf("timed out waiting for %s (worker run: %s %s)\n%s", what, state, runErr, buf[:n])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func workerFinding(t *testing.T, d *db.DB, fact string) (id int64, resolved bool, found bool) {
	t.Helper()
	var expires *float64
	err := d.Read.QueryRow("SELECT id, expires_at FROM kb_records WHERE origin = ?", "ops:"+fact).Scan(&id, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return id, expires != nil && *expires <= float64(time.Now().UnixNano())/1e9, true
}

func TestQuietPassMakesNoModelCall(t *testing.T) {
	fake := &fakeOpsGrafana{up: 1}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", grafanaHost(t, dir, srv.URL))
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	warm(t, api)
	requests := len(api.Requests())
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	runOpsSampler(t, f, d, samplesAtLeast(t, d, 6))
	runOpsSampler(t, f, d, samplesAtLeast(t, d, 12))
	if len(api.Requests()) != requests {
		t.Fatalf("a quiet pass made %d model calls", len(api.Requests())-requests)
	}
}

func TestOpsWorkerStopsAtItsMonthlyBudget(t *testing.T) {
	fake := &fakeOpsGrafana{up: 1}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", grafanaHost(t, dir, srv.URL))
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	seedSettings(t, map[string]string{serverops.KeyWorkerMonthlyUSD: "0"})
	warm(t, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	runOpsSampler(t, f, d, samplesAtLeast(t, d, 6))
	before := len(api.Requests())
	fake.setUp(0)
	runOpsSampler(t, f, d, samplerFinished(t, d))
	if len(api.Requests()) != before {
		t.Errorf("a spent budget still made %d model calls", len(api.Requests())-before)
	}
	ctx := config.With(settings.With(context.Background(), f.settings), f.state)
	result, err := attention.Query(ctx, d.Read, float64(time.Now().Unix()), serverops.AttentionSource(fixtureEnv(f)))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range result.Items {
		if item.Kind == "ops_budget" {
			found = true
		}
	}
	if !found {
		t.Errorf("attention has no budget item: %+v", result.Items)
	}
}
