package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"

	_ "github.com/jeiang/atrium/internal/db/readpaths/goldenpaths"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("realdb-gate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cacheDir := fs.String("cache-dir", os.Getenv("ATRIUM_CACHE_DIR"), "cache directory next to the database (ATRIUM_CACHE_DIR)")
	expect := fs.String("expect", "", "recorded readpaths.json to compare every path with (golden databases only)")
	bin := fs.String("atrium", "atrium", "atrium binary to serve and run the CLI checks with (go build -o /tmp/atrium ./cmd/atrium)")
	contactsDir := fs.String("contacts-dir", os.Getenv("ATRIUM_CONTACTS_DIR"), "contacts directory (ATRIUM_CONTACTS_DIR)")
	noE2E := fs.Bool("no-e2e", false, "skip the web and CLI checks")
	audit := fs.Bool("audit", false, "also list the tables no registered read path reads (renames each table on the scratch copy and reruns every path)")
	legacy := fs.Bool("legacy", false, "the database is the pre-migration legacy fixture: report schema changes instead of failing on them")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: realdb-gate [-legacy] [-audit] [-atrium BIN] [-no-e2e] [-cache-dir DIR] [-contacts-dir DIR] [-expect readpaths.json] COPY_OF_ATRIUM_DB")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	r := &report{out: stdout}
	if err := gate(ctx, r, options{src: fs.Arg(0), cacheDir: *cacheDir, contactsDir: *contactsDir, expect: *expect, bin: *bin, legacy: *legacy, audit: *audit, e2e: !*noE2E}); err != nil {
		r.fail("gate", err.Error())
	}
	return r.finish()
}

type report struct {
	out         io.Writer
	pass, skips int
	fails       []string
}

func (r *report) ok(name, detail string) {
	r.pass++
	_, _ = fmt.Fprintf(r.out, "PASS  %-45s %s\n", name, detail)
}

func (r *report) skip(name, detail string) {
	r.skips++
	_, _ = fmt.Fprintf(r.out, "SKIP  %-45s %s\n", name, detail)
}

func (r *report) fail(name, detail string) {
	r.fails = append(r.fails, name)
	_, _ = fmt.Fprintf(r.out, "FAIL  %-45s %s\n", name, detail)
}

func (r *report) finish() int {
	_, _ = fmt.Fprintf(r.out, "\n%d passed, %d skipped, %d failed\n", r.pass, r.skips, len(r.fails))
	if len(r.fails) > 0 {
		_, _ = fmt.Fprintf(r.out, "RESULT: FAIL (%s)\n", strings.Join(r.fails, ", "))
		return 1
	}
	_, _ = fmt.Fprintln(r.out, "RESULT: PASS")
	return 0
}

type options struct {
	src, cacheDir, contactsDir, expect, bin string
	legacy, audit, e2e                      bool
}

func gate(ctx context.Context, r *report, o options) error {
	src, cacheDir, expectFile, legacy := o.src, o.cacheDir, o.expect, o.legacy
	tmp, err := os.MkdirTemp("", "realdb-gate-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	path := filepath.Join(tmp, "atrium.db")
	if err := copyFile(src, path); err != nil {
		return err
	}
	if _, err := os.Stat(src + "-wal"); err == nil {
		r.fail("copy", src+"-wal exists: take the copy with sqlite3 .backup, not cp")
		return nil
	}
	r.ok("copy", fmt.Sprintf("%s -> scratch copy (the source is never opened)", src))

	if err := scrubEnv(path, cacheDir, o.contactsDir); err != nil {
		return err
	}
	d, err := db.Connect(path)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	check, err := scalarStrings(ctx, d.Read, "PRAGMA quick_check")
	if err != nil {
		return err
	}
	if len(check) == 1 && check[0] == "ok" {
		r.ok("quick_check", "ok")
	} else {
		r.fail("quick_check", strings.Join(check, "; "))
	}

	before, err := tables(ctx, d.Read)
	if err != nil {
		return err
	}
	if err := db.Migrate(ctx, d, path); err != nil {
		r.fail("db.Migrate", err.Error())
		return nil
	}
	after, err := tables(ctx, d.Read)
	if err != nil {
		return err
	}
	var added []string
	for _, t := range after {
		if !slices.Contains(before, t) {
			added = append(added, t)
		}
	}
	var removed []string
	for _, t := range before {
		if !slices.Contains(after, t) {
			removed = append(removed, t)
		}
	}
	switch {
	case legacy:
		r.ok("db.Migrate schema change", fmt.Sprintf("added: %s; removed: %s", listOrNone(added), listOrNone(removed)))
	case len(removed) > 0:
		r.fail("db.Migrate additive", "tables removed: "+strings.Join(removed, ", "))
	case len(added) == 0 || slices.Equal(added, []string{"schema_migrations"}):
		r.ok("db.Migrate additive", "added tables: "+listOrNone(added))
	default:
		r.fail("db.Migrate additive", "added tables beyond schema_migrations: "+strings.Join(added, ", "))
	}
	versions, err := scalarStrings(ctx, d.Read, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	r.ok("schema_migrations", "versions "+strings.Join(versions, ","))
	check, err = scalarStrings(ctx, d.Read, "PRAGMA quick_check")
	if err != nil {
		return err
	}
	if len(check) == 1 && check[0] == "ok" {
		r.ok("quick_check after migrate", "ok")
	} else {
		r.fail("quick_check after migrate", strings.Join(check, "; "))
	}

	var recorded map[string]json.RawMessage
	if expectFile != "" {
		raw, err := os.ReadFile(expectFile)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &recorded); err != nil {
			return err
		}
	}
	for _, p := range readpaths.All() {
		got, err := p.Result(ctx, d)
		if errors.Is(err, sql.ErrNoRows) && recorded == nil {
			r.skip(p.Name, "no matching row in this database")
			continue
		}
		if err != nil {
			r.fail(p.Name, err.Error())
			continue
		}
		if recorded == nil {
			r.ok(p.Name, fmt.Sprintf("%d bytes", len(got)))
			continue
		}
		want, err := p.Expected(recorded)
		if err != nil {
			r.fail(p.Name, err.Error())
			continue
		}
		if equalJSON(want, got) {
			r.ok(p.Name, "matches recorded result")
		} else {
			r.fail(p.Name, "differs from recorded result")
		}
	}
	var srv *e2e
	if o.e2e {
		srv, err = newE2E(ctx, o.bin, d)
		if err != nil {
			return err
		}
		defer srv.stop()
		if err := srv.start(ctx); err != nil {
			r.fail("atrium serve", err.Error())
			return nil
		}
		srv.checkRoutes(ctx, r)
		srv.checkCLI(ctx, r)
	}
	if o.audit {
		return auditTables(ctx, r, d, srv)
	}
	return nil
}

func auditTables(ctx context.Context, r *report, d *db.DB, srv *e2e) error {
	names, err := scalarStrings(ctx, d.Read, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'schema_migrations' ORDER BY name")
	if err != nil {
		return err
	}
	shadow := func(n string) bool {
		for _, v := range names {
			if n != v && strings.HasPrefix(n, v+"_") {
				return true
			}
		}
		return false
	}
	paths := readpaths.All()
	var unread []string
	for _, name := range names {
		if shadow(name) {
			continue
		}
		hidden := name + "__audit_hidden"
		if _, err := d.Write.ExecContext(ctx, "ALTER TABLE "+name+" RENAME TO "+hidden); err != nil {
			r.fail("audit "+name, err.Error())
			continue
		}
		read := false
		for _, p := range paths {
			if _, err := p.Result(ctx, d); err != nil && strings.Contains(err.Error(), "no such table") {
				read = true
				break
			}
		}
		if !read && srv != nil {
			read = srv.readsTable(ctx)
		}
		if _, err := d.Write.ExecContext(ctx, "ALTER TABLE "+hidden+" RENAME TO "+name); err != nil {
			return fmt.Errorf("audit: restoring %s: %w", name, err)
		}
		if !read {
			unread = append(unread, name)
		}
	}
	if len(unread) == 0 {
		r.ok("audit", "every table is read by a registered path or an end-to-end check")
		return nil
	}
	r.skip("audit", fmt.Sprintf("%d tables nothing reads: %s", len(unread), strings.Join(unread, ", ")))
	return nil
}

func equalJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func tables(ctx context.Context, q db.Querier) ([]string, error) {
	return scalarStrings(ctx, q, "SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
}

func scalarStrings(ctx context.Context, q db.Querier, query string) ([]string, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(rows *sql.Rows) (string, error) {
		var s string
		err := rows.Scan(&s)
		return s, err
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
