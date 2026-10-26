package main

import (
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/testutil/golden"
)

func TestRemovedMoneyModuleCancelsItsJobAndKeepsItsRows(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(golden.Dir(), "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	rows := func() (n int) {
		d := mustConnect(t, path)
		defer func() { _ = d.Close() }()
		if err := d.Read.QueryRow("SELECT (SELECT COUNT(*) FROM transactions) + (SELECT COUNT(*) FROM money_accounts) + (SELECT COUNT(*) FROM payees)").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := rows()

	base := startServeIn(t, dir)
	jar, _ := cookiejar.New(nil)
	code, body, _ := fetch(t, &http.Client{Jar: jar}, http.MethodGet, base+"/scheduled", nil)
	if code != http.StatusOK || strings.Contains(body, "fx-rates") || !strings.Contains(body, "media-sweep") {
		t.Fatalf("/scheduled: %d, fx-rates listed %v", code, strings.Contains(body, "fx-rates"))
	}
	if code, _, _ := fetch(t, &http.Client{Jar: jar}, http.MethodGet, base+"/money", nil); code != http.StatusNotFound {
		t.Errorf("/money: %d, want 404", code)
	}

	d := mustConnect(t, path)
	defer func() { _ = d.Close() }()
	var enabled, cancelled, live int
	if err := d.Read.QueryRow("SELECT enabled, cancelled FROM jobs WHERE name = 'fx-rates'").Scan(&enabled, &cancelled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 || cancelled != 1 {
		t.Errorf("fx-rates enabled=%d cancelled=%d, want a cancelled job", enabled, cancelled)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM jobs WHERE kind = 'builtin' AND cancelled = 0 AND enabled = 1").Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 6 {
		t.Errorf("%d built-in jobs still enabled, want 6", live)
	}
	if after := rows(); after != before || before == 0 {
		t.Errorf("money rows %d before, %d after; want them kept", before, after)
	}
}
