package mailtools_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/tool"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/py"
)

const tz = "America/Port_of_Spain"

var now = float64(time.Date(2026, 10, 15, 12, 0, 0, 0, mustZone(tz)).Unix())

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

type fields map[string]any

func tools(t *testing.T) map[string]tool.Tool {
	t.Helper()
	out := map[string]tool.Tool{}
	for _, tl := range mailtools.Tools() {
		out[tl.Name] = tl
	}
	return out
}

func loads(t *testing.T, text string) py.Dict {
	t.Helper()
	v, err := py.JSONLoads([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return v.(py.Dict)
}

func call(t *testing.T, tc *tool.Context, name, args string) (any, error) {
	t.Helper()
	tl, ok := tools(t)[name]
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	return tl.Handler(context.Background(), tc, loads(t, args))
}

func mustCall(t *testing.T, tc *tool.Context, name, args string) py.Dict {
	t.Helper()
	out, err := call(t, tc, name, args)
	if err != nil {
		t.Fatalf("%s(%s): %v", name, args, err)
	}
	d, ok := out.(py.Dict)
	if !ok {
		t.Fatalf("%s returned %T", name, out)
	}
	return d
}

func get(t *testing.T, d py.Dict, key string) any {
	t.Helper()
	v, ok := d.Get(key)
	if !ok {
		t.Fatalf("no key %q in %v", key, py.JSONDumps(d))
	}
	return v
}

func list(t *testing.T, v any) []any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("%T is not a list", v)
	}
	return l
}

func wantToolError(t *testing.T, err error, contains string) {
	t.Helper()
	var te tool.Error
	if !errors.As(err, &te) {
		t.Fatalf("error %v (%T) is not a tool.Error", err, err)
	}
	if contains != "" && te.Error() != contains {
		t.Errorf("error %q, want %q", te, contains)
	}
}

func newDB(t *testing.T) *db.DB { return dbtest.Mail(t) }

func ctxFor(d *db.DB, cfg config.Config, env map[string]string) *tool.Context {
	return &tool.Context{
		DB: d, Cfg: cfg, Env: config.Map(env), Kind: tool.Chat, ThreadID: 1, RunID: 1,
		Now: func() float64 { return now },
	}
}

func exec(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.Write.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func asRaised(err error, target **tool.Raised) bool { return errors.As(err, target) }

func asNotConfigured(err error, target *tool.NotConfigured) bool { return errors.As(err, target) }
