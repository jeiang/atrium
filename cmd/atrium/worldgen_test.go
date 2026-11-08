package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

type notionVector struct {
	RowID      int64  `json:"rowid"`
	Visibility string `json:"visibility"`
	Embedding  []byte `json:"embedding"`
}

func TestGenerateWorldFixture(t *testing.T) {
	if os.Getenv("ATRIUM_GENERATE_WORLD") == "" {
		t.Skip("set ATRIUM_GENERATE_WORLD=1 to regenerate testdata/golden/world.db")
	}
	dir := t.TempDir()
	world := filepath.Join(dir, "atrium.db")
	t.Setenv("ATRIUM_DB", world)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_CONFIG", "")
	t.Setenv("ATRIUM_DB_KEY", "")
	app := defaultApp()
	run := func(argv ...string) {
		var out, errs strings.Builder
		if code := app.Run(context.Background(), argv, &out, &errs); code != cli.ExitOK {
			t.Fatalf("%v: exit %d stdout %q stderr %q", argv, code, out.String(), errs.String())
		}
	}
	source := filepath.Join(golden.Dir(), "atrium.db")
	run("db", "import-v1", source)
	seedWorld(t, app, world, source)
	d, err := db.Connect(world)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"embed_message_0", "embed_notion_0"} {
		if _, err := d.Write.Exec("DELETE FROM " + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Write.Exec("VACUUM"); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	copyFile(t, world, filepath.Join(golden.Dir(), "world.db"))

	v1, err := db.OpenSource(source, db.Key{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v1.Close() }()
	rows, err := v1.Query("SELECT rowid, visibility, embedding FROM notion_vec")
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := db.Collect(rows, func(r *sql.Rows) (notionVector, error) {
		var v notionVector
		return v, r.Scan(&v.RowID, &v.Visibility, &v.Embedding)
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(golden.Dir(), "notion_vec.json"), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedWorld(t *testing.T, app *cli.App, target, source string) {
	t.Helper()
	v2, err := db.Connect(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v2.Close() }()
	v1, err := db.OpenSource(source, db.Key{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v1.Close() }()
	columns := func(q db.Querier, table string) []string {
		rows, err := q.QueryContext(context.Background(), "SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			t.Fatal(err)
		}
		names, err := db.Collect(rows, func(row *sql.Rows) (string, error) {
			var name string
			return name, row.Scan(&name)
		})
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	rows, err := v2.Read.QueryContext(context.Background(), "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	tables, err := db.Collect(rows, func(row *sql.Rows) (string, error) {
		var name string
		return name, row.Scan(&name)
	})
	if err != nil {
		t.Fatal(err)
	}
	var stmts []string
	for _, table := range tables {
		if seededTables[table] {
			continue
		}
		if strings.HasPrefix(table, "message_fts_") || strings.HasPrefix(table, "thread_fts_") || strings.HasPrefix(table, "notion_fts_") {
			continue
		}
		v1cols := columns(v1, table)
		if len(v1cols) == 0 {
			continue
		}
		have := map[string]bool{}
		for _, c := range v1cols {
			have[c] = true
		}
		var common []string
		for _, c := range columns(v2.Read, table) {
			if have[c] {
				common = append(common, c)
			}
		}
		if len(common) == 0 {
			continue
		}
		quoted := make([]string, len(common))
		for i, c := range common {
			quoted[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
		}
		if ftsTables[table] != nil {
			stmts = append(stmts, "INSERT INTO main."+table+" (rowid, "+strings.Join(quoted, ", ")+") SELECT rowid, "+
				strings.Join(quoted, ", ")+" FROM v1."+table+";")
			continue
		}
		stmts = append(stmts, "INSERT OR IGNORE INTO main."+table+" ("+strings.Join(quoted, ", ")+") SELECT "+
			strings.Join(quoted, ", ")+" FROM v1."+table+";")
	}
	script := "PRAGMA foreign_keys = OFF;\nATTACH DATABASE '" + strings.ReplaceAll(source, "'", "''") + "' AS v1;\n" +
		strings.Join(stmts, "\n") + "\nDETACH DATABASE v1;\n"
	saved := app.Stdin
	app.Stdin = strings.NewReader(script)
	defer func() { app.Stdin = saved }()
	var out, errs strings.Builder
	if code := app.Run(context.Background(), []string{"db", "shell"}, &out, &errs); code != cli.ExitOK {
		t.Fatalf("seed the world: exit %d stdout %q stderr %q", code, out.String(), errs.String())
	}
}
