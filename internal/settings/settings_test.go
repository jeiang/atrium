package settings_test

import (
	"errors"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/settings"
)

type pins map[string][2]string

func (p pins) Pin(key string) (string, string, bool) {
	v, ok := p[key]
	return v[0], v[1], ok
}

func registry(t *testing.T) *settings.Registry {
	t.Helper()
	r := settings.New()
	both := []settings.Scope{settings.Instance, settings.Thread}
	err := r.Register(
		settings.Def{Key: "plain", Module: "test", Type: settings.Int, Default: "10", Class: settings.UI, Scopes: both, ThreadColumn: "context_limit"},
		settings.Def{
			Key: "cap", Module: "test", Type: settings.Int, Default: "10", Class: settings.Cap, Scopes: both,
			ThreadColumn: "context_limit", Check: settings.Between(1, 100),
		},
		settings.Def{Key: "instance_only", Module: "test", Type: settings.Int, Default: "3", Class: settings.Advanced, Scopes: []settings.Scope{settings.Instance}},
		settings.Def{Key: "thread_only", Module: "test", Type: settings.Int, Default: "4", Class: settings.Advanced, Scopes: []settings.Scope{settings.Thread}, ThreadColumn: "context_limit"},
		settings.Def{Key: "bootstrap", Module: "test", Type: settings.String, Default: "x", Class: settings.Bootstrap},
	)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newThread(t *testing.T, d *db.DB) int64 {
	t.Helper()
	res, err := d.Write.Exec("INSERT INTO threads (kind, effort, created_at, updated_at) VALUES ('chat', 'medium', 0, 0)")
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestResolveOrderScopesAndCaps(t *testing.T) {
	type write struct {
		key, value string
		thread     bool
	}
	cases := []struct {
		name   string
		pin    map[string][2]string
		writes []write
		key    string
		thread bool
		want   string
		origin settings.Origin
		source string
	}{
		{"default", nil, nil, "plain", true, "10", settings.FromDefault, ""},
		{"instance beats default", nil, []write{{"plain", "20", false}}, "plain", true, "20", settings.FromInstance, ""},
		{"thread beats instance", nil, []write{{"plain", "20", false}, {"plain", "30", true}}, "plain", true, "30", settings.FromThread, ""},
		{"thread value ignored at instance level", nil, []write{{"plain", "30", true}}, "plain", false, "10", settings.FromDefault, ""},
		{"pin beats thread, instance and default", map[string][2]string{"plain": {"99", "env ATRIUM_PLAIN"}}, []write{{"plain", "20", false}, {"plain", "30", true}}, "plain", true, "99", settings.FromPin, "env ATRIUM_PLAIN"},
		{"cap takes the thread value when lower", nil, []write{{"cap", "8", false}, {"cap", "5", true}}, "cap", true, "5", settings.FromThread, ""},
		{"cap keeps the instance value when the thread asks for more", nil, []write{{"cap", "8", false}, {"cap", "20", true}}, "cap", true, "8", settings.FromInstance, ""},
		{"cap keeps the default when the thread asks for more", nil, []write{{"cap", "20", true}}, "cap", true, "10", settings.FromDefault, ""},
		{"cap pin beats a lower thread value", map[string][2]string{"cap": {"50", "toml"}}, []write{{"cap", "5", true}}, "cap", true, "50", settings.FromPin, "toml"},
		{"thread-only key ignores a stray instance row", nil, []write{{"thread_only", "30", true}}, "thread_only", false, "4", settings.FromDefault, ""},
		{"bootstrap key is its default", nil, nil, "bootstrap", false, "x", settings.FromDefault, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := dbtest.Schema(t)
			r := registry(t)
			ctx := t.Context()
			id := newThread(t, d)
			if _, err := d.Write.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES ('thread_only', '77')"); err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.writes {
				var thread int64
				if w.thread {
					thread = id
				}
				if err := r.Set(ctx, d.Write, w.key, w.value, thread); err != nil {
					t.Fatalf("Set(%s=%s): %v", w.key, w.value, err)
				}
			}
			if tc.pin != nil {
				r.SetPins(pins(tc.pin))
			}
			var thread int64
			if tc.thread {
				thread = id
			}
			got, err := r.Resolve(ctx, d.Read, tc.key, thread)
			if err != nil {
				t.Fatal(err)
			}
			if got.Value != tc.want || got.Origin != tc.origin || got.PinSource != tc.source || got.Pinned() != (tc.origin == settings.FromPin) {
				t.Errorf("Resolve(%s) = %+v, want %s from %s (%q)", tc.key, got, tc.want, tc.origin, tc.source)
			}
		})
	}
}

func TestScopesAreEnforced(t *testing.T) {
	d := dbtest.Schema(t)
	r := registry(t)
	ctx := t.Context()
	id := newThread(t, d)
	var scope *settings.ScopeError
	if err := r.Set(ctx, d.Write, "instance_only", "5", id); !errors.As(err, &scope) || scope.Scope != settings.Thread {
		t.Errorf("thread write of an instance-only key: %v", err)
	}
	if err := r.Set(ctx, d.Write, "thread_only", "5", 0); !errors.As(err, &scope) || scope.Scope != settings.Instance {
		t.Errorf("instance write of a thread-only key: %v", err)
	}
	if err := r.Set(ctx, d.Write, "bootstrap", "y", 0); !errors.As(err, &scope) {
		t.Errorf("write of a bootstrap key: %v", err)
	}
	if _, err := r.Resolve(ctx, d.Read, "instance_only", id); !errors.As(err, &scope) {
		t.Errorf("thread resolve of an instance-only key: %v", err)
	}
	if err := r.Set(ctx, d.Write, "plain", "5", id+1); !errors.Is(err, settings.UnknownThread(id+1)) {
		t.Errorf("write at a missing thread: %v", err)
	}
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM settings").Scan(&n); err != nil || n != 0 {
		t.Errorf("rejected writes left %d rows, %v", n, err)
	}
}

func TestPinnedKeyIsReadOnlyAtEveryLevel(t *testing.T) {
	d := dbtest.Schema(t)
	r := registry(t)
	ctx := t.Context()
	id := newThread(t, d)
	r.SetPins(pins{"plain": {"99", "file /etc/atrium.toml"}})
	for _, thread := range []int64{0, id} {
		var pinned *settings.PinnedError
		err := r.Set(ctx, d.Write, "plain", "5", thread)
		if !errors.As(err, &pinned) || pinned.Key != "plain" || pinned.Source != "file /etc/atrium.toml" {
			t.Errorf("Set at thread %d: %v", thread, err)
		}
	}
	got, err := r.Resolve(ctx, d.Read, "plain", id)
	if err != nil || !got.Pinned() || got.Value != "99" || got.PinSource != "file /etc/atrium.toml" {
		t.Errorf("Resolve = %+v, %v", got, err)
	}
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM settings").Scan(&n); err != nil || n != 0 {
		t.Errorf("pinned writes left %d rows, %v", n, err)
	}
}

func TestOneValidationMessageForEveryCaller(t *testing.T) {
	d := dbtest.Schema(t)
	r := registry(t)
	ctx := t.Context()
	for value, want := range map[string]string{
		"abc": "cap must be a whole number",
		"0":   "cap must be between 1 and 100",
		"101": "cap must be between 1 and 100",
	} {
		form := r.Validate("cap", value)
		cli := r.Set(ctx, d.Write, "cap", value, 0)
		if form == nil || cli == nil || form.Error() != want || cli.Error() != want {
			t.Errorf("value %q: validate %v, set %v, want %q", value, form, cli, want)
		}
	}
	if err := r.Validate("cap", "50"); err != nil {
		t.Errorf("valid value refused: %v", err)
	}
}

func TestRegisterRefusesBadDefinitions(t *testing.T) {
	r := registry(t)
	if err := r.Register(settings.Def{Key: "plain", Module: "test", Type: settings.String, Class: settings.Bootstrap}); !errors.Is(err, settings.DuplicateKey("plain")) {
		t.Errorf("duplicate: %v", err)
	}
	bad := map[string]settings.Def{
		"cap needs a number":     {Key: "a", Module: "m", Type: settings.String, Class: settings.Cap, Scopes: []settings.Scope{settings.Instance}},
		"thread needs column":    {Key: "b", Module: "m", Type: settings.Int, Class: settings.UI, Scopes: []settings.Scope{settings.Thread}},
		"bootstrap has scopes":   {Key: "c", Module: "m", Type: settings.String, Class: settings.Bootstrap, Scopes: []settings.Scope{settings.Instance}},
		"default must pass":      {Key: "d", Module: "m", Type: settings.Int, Default: "x", Class: settings.UI, Scopes: []settings.Scope{settings.Instance}},
		"secret goes with class": {Key: "e", Module: "m", Type: settings.String, Class: settings.UI, Secret: true, Scopes: []settings.Scope{settings.Instance}},
	}
	for name, def := range bad {
		var invalid settings.InvalidDef
		if err := r.Register(def); !errors.As(err, &invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.Resolve(t.Context(), nil, "nope", 0); !errors.Is(err, settings.UnknownKey("nope")) {
		t.Errorf("unknown key: %v", err)
	}
}
