package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/golden"
	"github.com/jeiang/atrium/sdk"
)

const extName = "extdemo"

type extModule struct{}

func init() {
	sdk.Register(extName, extModule{})
	sdk.RegisterMigrations(extName, sdk.Migration{Version: 1, Apply: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE extdemo_pings (id INTEGER PRIMARY KEY, note TEXT NOT NULL)")
		return err
	}})
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
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	t.Setenv(flagEnv(extName), "")

	off := runApp(extName)
	if off.code != cli.ExitOK || off.stdout != "" {
		t.Fatalf("disabled module: exit %d stdout %q stderr %q", off.code, off.stdout, off.stderr)
	}
	if !tableExists(t, dir, "extdemo_pings") {
		t.Fatal("the module migration did not run while the module was off")
	}
	if n := extPings(t, dir); n != 0 {
		t.Fatalf("disabled module wrote %d rows", n)
	}

	if set := runApp("settings", "set", extName+"_enabled", "true"); set.code != cli.ExitOK {
		t.Fatalf("enable: exit %d stderr %q", set.code, set.stderr)
	}
	on := runApp(extName)
	if on.code != cli.ExitOK || on.stdout != "pong\n" {
		t.Fatalf("enabled module: exit %d stdout %q stderr %q", on.code, on.stdout, on.stderr)
	}
	if n := extPings(t, dir); n != 1 {
		t.Fatalf("enabled module wrote %d rows, want 1", n)
	}
}

func TestExternalModuleMigratesAGoldenDatabaseCopy(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	t.Setenv(flagEnv(extName), "")
	copyGoldenFile(t, filepath.Join(golden.Dir(), "atrium.db"), filepath.Join(dir, "atrium.db"))

	ran := runApp(extName)
	if ran.code != cli.ExitOK {
		t.Fatalf("golden copy: exit %d stderr %q", ran.code, ran.stderr)
	}
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var core, mod int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = 10").Scan(&core); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM module_migrations WHERE module = ? AND version = 1", extName).Scan(&mod); err != nil {
		t.Fatal(err)
	}
	if core != 1 || mod != 1 {
		t.Fatalf("golden copy: core migration 10 recorded %d, module migration recorded %d", core, mod)
	}
}

func TestNewerExternalModuleSchemaIsRefusedBeforeWrites(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	t.Setenv(flagEnv(extName), "true")
	if first := runApp(extName); first.code != cli.ExitOK {
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

	refused := runApp(extName)
	if refused.code == cli.ExitOK || !strings.Contains(refused.stderr, "newer "+extName+" module") {
		t.Fatalf("newer module schema: exit %d stderr %q", refused.code, refused.stderr)
	}
	if n := extPings(t, dir); n != 1 {
		t.Fatalf("refused run left %d rows, want 1", n)
	}
}
