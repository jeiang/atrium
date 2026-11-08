package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/sdk"
)

const extName = "extdemo"

type extModule struct{}

var extOnce sync.Once

func extModules() []module.Module {
	extOnce.Do(func() {
		settings.MustRegister(module.FlagDef(extName))
		sdk.RegisterMigrations(extName, sdk.Migration{Version: 1, Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "CREATE TABLE extdemo_pings (id INTEGER PRIMARY KEY, note TEXT NOT NULL)")
			return err
		}})
	})
	return []module.Module{extModule{}}
}

func (extModule) Register(sdk.Context) sdk.Registration {
	return sdk.Registration{Name: extName, Commands: []*sdk.Command{{
		Name: extName,
		Setup: func(*sdk.Args) sdk.Handler {
			return func(env *sdk.Env) int {
				d, _, err := sdk.OpenDB(env)
				if err != nil {
					return sdk.Fail(env, err)
				}
				defer func() { _ = d.Close() }()
				if err := d.WithTx(env.Ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(env.Ctx, "INSERT INTO extdemo_pings (note) VALUES ('ping')")
					return err
				}); err != nil {
					return sdk.Fail(env, err)
				}
				_, _ = fmt.Fprintln(env.Stdout, "pong")
				return sdk.ExitOK
			}
		},
	}}}
}

func extPings(t *testing.T, dir string) int {
	t.Helper()
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM extdemo_pings").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExternalModuleMigratesAndRunsOnlyWhenEnabled(t *testing.T) {
	mods := extModules()
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	activeFixture.env[flagEnv(extName)] = ""

	off := runAppWith(mods, extName)
	if off.code != cli.ExitOK || off.stdout != "" {
		t.Fatalf("disabled module: exit %d stdout %q stderr %q", off.code, off.stdout, off.stderr)
	}
	if !tableExists(t, dir, "extdemo_pings") {
		t.Fatal("the module migration did not run while the module was off")
	}
	if n := extPings(t, dir); n != 0 {
		t.Fatalf("disabled module wrote %d rows", n)
	}

	if set := runAppWith(mods, "settings", "set", extName+"_enabled", "true"); set.code != cli.ExitOK {
		t.Fatalf("enable: exit %d stderr %q", set.code, set.stderr)
	}
	on := runAppWith(mods, extName)
	if on.code != cli.ExitOK || on.stdout != "pong\n" {
		t.Fatalf("enabled module: exit %d stdout %q stderr %q", on.code, on.stdout, on.stderr)
	}
	if n := extPings(t, dir); n != 1 {
		t.Fatalf("enabled module wrote %d rows, want 1", n)
	}
}

func TestNewerExternalModuleSchemaIsRefusedBeforeWrites(t *testing.T) {
	mods := extModules()
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	activeFixture.env[flagEnv(extName)] = "true"
	if first := runAppWith(mods, extName); first.code != cli.ExitOK {
		t.Fatalf("first run: exit %d stderr %q", first.code, first.stderr)
	}
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO module_migrations (module, version, applied_at) VALUES (?, 2, 0)", extName); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	refused := runAppWith(mods, extName)
	if refused.code == cli.ExitOK || !strings.Contains(refused.stderr, "newer "+extName+" module") {
		t.Fatalf("newer module schema: exit %d stderr %q", refused.code, refused.stderr)
	}
	if n := extPings(t, dir); n != 1 {
		t.Fatalf("refused run left %d rows, want 1", n)
	}
}
