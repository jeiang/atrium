package openloops_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/py"
)

func TestRefreshRunsAllStepsAndIsolatesFailures(t *testing.T) {
	d := newDB(t)
	mail(t, d, 1, 2, nil)
	judge := &fakeJudge{}
	out := openloops.Refresh(ctx, d, config.Config{}, NOW(), openloops.Options{Env: env(), Judge: judge.judge, Clock: NOW})
	if got := py.JSONDumps(out, py.JSONOpts{Compact: true}); got != `{"backfilled":1,"loops":{"owe_reply":1,"waiting_on_them":0},"commitments":0,"reminders":0}` {
		t.Errorf("refresh = %s", got)
	}
	mail(t, d, 2, 1, fields{"subject": "Second"})
	boom := func(context.Context, config.Env, string, openloops.Message, string) (any, error) {
		return nil, errors.New("model exploded")
	}
	out = openloops.Refresh(ctx, d, config.Config{}, NOW(), openloops.Options{Env: env(), Judge: boom, Clock: NOW})
	if got := py.JSONDumps(out, py.JSONOpts{Compact: true}); got != `{"backfilled":1,"loops":{"owe_reply":2,"waiting_on_them":0},"commitments":null,"reminders":0}` {
		t.Errorf("refresh with a failing model = %s", got)
	}
}

func TestBackfillFillsHeadersFromRawFiles(t *testing.T) {
	d := newDB(t)
	cache := t.TempDir()
	raw := filepath.Join(cache, "raw", "a.eml")
	if err := os.MkdirAll(filepath.Dir(raw), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(raw, []byte("From: a@b.test\r\nIn-Reply-To: <p1@x.test>\r\nReferences: <p0@x.test> <p1@x.test>\r\n\r\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	row := mail(t, d, 1, 1, fields{"raw_path": "raw/a.eml"})
	missing := mail(t, d, 2, 1, fields{"raw_path": "raw/gone.eml"})
	exec(t, d, "UPDATE messages SET in_reply_to = NULL, refs = NULL")
	n, err := openloops.BackfillHeaders(ctx, d, config.Config{CacheDir: cache}, NOW(), openloops.BackfillLimit)
	if err != nil || n != 2 {
		t.Fatalf("BackfillHeaders = %d, %v", n, err)
	}
	for id, want := range map[int64][2]string{row: {"p1@x.test", "p0@x.test p1@x.test"}, missing: {"", ""}} {
		var got [2]string
		if err := d.Read.QueryRow("SELECT in_reply_to, refs FROM messages WHERE id = ?", id).Scan(&got[0], &got[1]); err != nil || got != want {
			t.Errorf("headers of %d = %q, %v; want %q", id, got, err, want)
		}
	}
	if n, err := openloops.BackfillHeaders(ctx, d, config.Config{CacheDir: cache}, NOW(), openloops.BackfillLimit); err != nil || n != 0 {
		t.Errorf("second pass = %d, %v", n, err)
	}
}

func TestBackfillStopsAtTheHeaderBlock(t *testing.T) {
	d := newDB(t)
	cache := t.TempDir()
	body := "From: a@b.test\nIn-Reply-To: <p1@x.test>\n\nContent-Type: multipart/mixed\nReferences: <no@x.test>\n"
	if err := os.WriteFile(filepath.Join(cache, "a.eml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	row := mail(t, d, 1, 1, fields{"raw_path": "a.eml"})
	exec(t, d, "UPDATE messages SET in_reply_to = NULL")
	if _, err := openloops.BackfillHeaders(ctx, d, config.Config{CacheDir: cache}, NOW(), 10); err != nil {
		t.Fatal(err)
	}
	var in, refs string
	if err := d.Read.QueryRow("SELECT in_reply_to, refs FROM messages WHERE id = ?", row).Scan(&in, &refs); err != nil || in != "p1@x.test" || strings.Contains(refs, "no@") {
		t.Errorf("headers = %q %q, %v", in, refs, err)
	}
}

func TestBackfillReadsBareCarriageReturnsAndFailsOnAnUnparsableContentType(t *testing.T) {
	d := newDB(t)
	cache := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(cache, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("cr.eml", "In-Reply-To: <p1@x.test>\rReferences: <p0@x.test>\r\rbody")
	row := mail(t, d, 1, 1, fields{"raw_path": "cr.eml"})
	exec(t, d, "UPDATE messages SET in_reply_to = NULL, refs = NULL")
	cfg := config.Config{CacheDir: cache}
	if n, err := openloops.BackfillHeaders(ctx, d, cfg, NOW(), 10); err != nil || n != 1 {
		t.Fatalf("BackfillHeaders = %d, %v", n, err)
	}
	var in, refs string
	if err := d.Read.QueryRow("SELECT in_reply_to, refs FROM messages WHERE id = ?", row).Scan(&in, &refs); err != nil || in != "p1@x.test" || refs != "p0@x.test" {
		t.Errorf("headers = %q %q, %v", in, refs, err)
	}
	write("bad.eml", "In-Reply-To: <p2@x.test>\r\nContent-Type: text/plain; name*=\"'\"\r\n\r\nbody")
	bad := mail(t, d, 2, 1, fields{"raw_path": "bad.eml"})
	exec(t, d, "UPDATE messages SET in_reply_to = NULL WHERE id = ?", bad)
	if _, err := openloops.BackfillHeaders(ctx, d, cfg, NOW(), 10); err == nil {
		t.Error("an unparsable Content-Type must fail the step as it does in Python")
	}
	out := openloops.Refresh(ctx, d, cfg, NOW(), openloops.Options{Env: env(), Judge: (&fakeJudge{}).judge, Clock: NOW})
	if v, _ := out.Get("backfilled"); v != nil {
		t.Errorf("backfilled = %v, want null", v)
	}
	if v, _ := out.Get("loops"); v == nil {
		t.Error("the other steps still run")
	}
}
