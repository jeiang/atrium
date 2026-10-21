package module_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
)

type stub struct{ name, key string }

func (s stub) Register(module.Context) module.Registration {
	r := module.Registration{
		Name: s.name,
		Commands: []*cli.Command{{Name: s.name, Setup: func(*cli.Args) cli.Handler {
			return func(env *cli.Env) int {
				_, _ = env.Stdout.Write([]byte("ran " + s.name + "\n"))
				return cli.ExitOK
			}
		}}},
	}
	if s.key != "" {
		r.Requires = []module.Requirement{module.Setting(s.key)}
	}
	return r
}

type rig struct {
	t    *testing.T
	path string
	app  *cli.App
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	reg := settings.New()
	err := reg.Register(
		module.FlagDef("alpha"), module.FlagDef("beta"),
		settings.Def{Key: "alpha_url", Module: "core", Type: settings.String, Class: settings.UI, Scopes: []settings.Scope{settings.Instance}},
		settings.Def{Key: "beta_url", Module: "core", Type: settings.String, Class: settings.UI, Scopes: []settings.Scope{settings.Instance}},
	)
	if err != nil {
		t.Fatal(err)
	}
	env := config.Map(map[string]string{"ATRIUM_DB": path, "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache")})
	set, err := module.NewSet(reg, env,
		stub{name: "core"}, stub{name: "alpha", key: "alpha_url"}, stub{name: "beta", key: "beta_url"})
	if err != nil {
		t.Fatal(err)
	}
	app := cli.New("atrium", nil)
	app.Add(set.Commands()...)
	r := &rig{t: t, path: path, app: app}
	d := r.open()
	if err := db.Migrate(context.Background(), d, path); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	return r
}

func (r *rig) open() *db.DB {
	r.t.Helper()
	d, err := db.Connect(r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	return d
}

func (r *rig) exec(query string, args ...any) {
	r.t.Helper()
	d := r.open()
	defer func() { _ = d.Close() }()
	if _, err := d.Write.ExecContext(context.Background(), query, args...); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) count(query string, args ...any) int {
	r.t.Helper()
	d := r.open()
	defer func() { _ = d.Close() }()
	var n int
	if err := d.Read.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) run(name string) string {
	r.t.Helper()
	var out, errOut bytes.Buffer
	if code := r.app.Run(context.Background(), []string{name}, &out, &errOut); code != cli.ExitOK {
		r.t.Fatalf("%s: exit %d %q", name, code, errOut.String())
	}
	return out.String()
}

func (r *rig) migrationRows() int {
	return r.count("SELECT COUNT(*) FROM settings_audit WHERE source = 'migration'")
}

func TestPhaseADatabaseAdoptsModulesWhoseRequirementsHold(t *testing.T) {
	r := newRig(t)
	r.exec("INSERT INTO settings (key, value) VALUES ('alpha_url', 'http://alpha.test')")

	if got := r.run("alpha"); got != "ran alpha\n" {
		t.Fatalf("alpha = %q, want it to run", got)
	}
	if got := r.run("beta"); got != "" {
		t.Fatalf("beta = %q, want it skipped for its unset requirement", got)
	}
	if got := r.run("core"); got != "ran core\n" {
		t.Fatalf("core = %q", got)
	}
	for key, want := range map[string]int{"alpha_enabled": 1, "beta_enabled": 1, "modules_seeded": 1} {
		if n := r.count("SELECT COUNT(*) FROM settings_audit WHERE key = ? AND source = 'migration'", key); n != want {
			t.Errorf("migration audit rows for %s = %d, want %d", key, n, want)
		}
	}
	if n := r.count("SELECT COUNT(*) FROM settings WHERE key = 'alpha_enabled' AND value = 'true'"); n != 1 {
		t.Errorf("alpha_enabled is not true")
	}
	if n := r.count("SELECT COUNT(*) FROM settings WHERE key = 'beta_enabled' AND value = 'false'"); n != 1 {
		t.Errorf("beta_enabled is not false")
	}

	rows := r.migrationRows()
	r.run("alpha")
	if got := r.migrationRows(); got != rows {
		t.Errorf("rerun wrote %d migration rows, want none", got-rows)
	}

	r.exec("DELETE FROM settings WHERE key = 'alpha_url'")
	if got := r.run("alpha"); got != "" || strings.Contains(got, "ran") {
		t.Fatalf("alpha after its setting vanished = %q, want it skipped", got)
	}
	if n := r.count("SELECT COUNT(*) FROM settings WHERE key = 'alpha_enabled'"); n != 1 {
		t.Errorf("the module flag row was lost")
	}
}

func TestEmptyDatabaseLeavesOnlyCoreOnAndIsNeverAdoptedLater(t *testing.T) {
	r := newRig(t)
	if got := r.run("core"); got != "ran core\n" {
		t.Fatalf("core = %q", got)
	}
	if got := r.run("alpha"); got != "" {
		t.Fatalf("alpha = %q, want off", got)
	}
	r.exec("INSERT INTO settings (key, value) VALUES ('alpha_url', 'http://alpha.test')")
	if got := r.run("alpha"); got != "" {
		t.Fatalf("alpha = %q, want it to stay off until an operator enables it", got)
	}
	if n := r.count("SELECT COUNT(*) FROM settings WHERE key LIKE '%_enabled'"); n != 0 {
		t.Errorf("empty-database start wrote %d flag rows", n)
	}
}
