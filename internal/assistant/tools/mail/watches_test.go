package mailtools_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/py"
)

func TestPackagesTool(t *testing.T) {
	d := newDB(t)
	dbtest.AddMessage(t, d, 1, "1Z999AA10123456784", fields{
		"from_addr": "billing@shop.test", "from_domain": "shop.test", "date_ts": now - day, "subject": "Shipped", "k_order": 1,
	})
	cfg := config.Config{}
	if _, err := watches.ScanPackages(t.Context(), d, cfg, watches.ScanLimit, now, watches.Options{Env: config.Map(map[string]string{"ATRIUM_TZ": tz}), Chat: func(context.Context, string, string, string, any, int) (any, error) {
		return py.Dict{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	tc := ctxFor(d, cfg, nil)
	out := mustCall(t, tc, "packages", `{}`)
	first := list(t, get(t, out, "packages"))[0].(py.Dict)
	if get(t, first, "tracking_number") != "1Z999AA10123456784" {
		t.Errorf("first = %s", py.JSONDumps(first))
	}
	if note := get(t, out, "note"); note != "Status comes only from emails the operator received; there is no carrier lookup, so it can lag." {
		t.Errorf("note = %v", note)
	}
	_, err := call(t, tc, "packages", `{"active_only": "yes"}`)
	wantToolError(t, err, "active_only must be true or false")
	if ok, err := watches.DismissPackage(t.Context(), d.Write, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if got := list(t, get(t, mustCall(t, tc, "packages", `{}`), "packages")); len(got) != 0 {
		t.Errorf("dismissed package listed: %v", got)
	}
	if got := list(t, get(t, mustCall(t, tc, "packages", `{"active_only": false}`), "packages")); len(got) != 0 {
		t.Errorf("dismissed package listed with active_only false: %v", got)
	}
}

func TestUpcomingBirthdaysTool(t *testing.T) {
	d := newDB(t)
	dir := filepath.Join(t.TempDir(), "cards")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.vcf"), []byte("BEGIN:VCARD\nVERSION:3.0\nFN:Ada\nBDAY:--1017\nEND:VCARD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := ctxFor(d, config.Config{ContactsDir: dir}, map[string]string{"ATRIUM_TZ": tz})
	out := mustCall(t, tc, "upcoming_birthdays", `{}`)
	first := list(t, get(t, out, "upcoming"))[0].(py.Dict)
	if got := py.JSONDumps(first); got != `{"name": "Ada", "kind": "birthday", "label": "birthday", "date": "2026-10-17", "days_until": 2}` {
		t.Errorf("first = %s", got)
	}
	if got := list(t, get(t, mustCall(t, tc, "upcoming_birthdays", `{"days": 0}`), "upcoming")); len(got) != 0 {
		t.Errorf("days 0: %v", got)
	}
	bare := ctxFor(d, config.Config{}, nil)
	_, err := call(t, bare, "upcoming_birthdays", `{}`)
	var nc tool.NotConfigured
	if !asNotConfigured(err, &nc) || nc != "ATRIUM_CONTACTS_DIR is not set" {
		t.Errorf("err = %v", err)
	}
}

func TestUpcomingBirthdaysTurningAndDismissal(t *testing.T) {
	d := newDB(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.vcf"), []byte("BEGIN:VCARD\nVERSION:3.0\nFN:Ada\nBDAY:1990-10-17\nEND:VCARD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := ctxFor(d, config.Config{ContactsDir: dir}, map[string]string{"ATRIUM_TZ": tz})
	first := list(t, get(t, mustCall(t, tc, "upcoming_birthdays", `{"days": 5}`), "upcoming"))[0].(py.Dict)
	if get(t, first, "turning") != int64(36) {
		t.Errorf("first = %s", py.JSONDumps(first))
	}
	if _, ok := first.Get("emails"); ok {
		t.Error("emails leaked")
	}
	if _, ok := first.Get("key"); ok {
		t.Error("key leaked")
	}
	_, err := call(t, tc, "upcoming_birthdays", `{"days": "x"}`)
	wantToolError(t, err, "days must be a number")
}

func TestPackagesUnknownTimezoneKeepsItsClass(t *testing.T) {
	d := newDB(t)
	dbtest.AddMessage(t, d, 1, "1Z999AA10123456784", fields{
		"from_addr": "billing@shop.test", "from_domain": "shop.test", "date_ts": now - day, "subject": "Shipped", "k_order": 1,
	})
	env := map[string]string{"ATRIUM_TZ": "Mars/Base"}
	chat := func(context.Context, string, string, string, any, int) (any, error) { return py.Dict{}, nil }
	if _, err := watches.ScanPackages(t.Context(), d, config.Config{}, watches.ScanLimit, now, watches.Options{Env: config.Map(map[string]string{"ATRIUM_TZ": tz}), Chat: chat}); err != nil {
		t.Fatal(err)
	}
	_, err := call(t, ctxFor(d, config.Config{}, env), "packages", `{}`)
	var classed py.Classed
	if !errors.As(err, &classed) || classed.PyClass() != "ZoneInfoNotFoundError" {
		t.Errorf("err = %v (%T)", err, err)
	}
}
