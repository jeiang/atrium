package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestGoldenSQLiteVersion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "db", "sqlite_version.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded string
	for line := range strings.Lines(string(raw)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "sqlite "); ok {
			recorded = v
		}
	}
	if recorded == "" {
		t.Fatal("sqlite_version.txt has no sqlite line")
	}
	var bundled string
	if err := openTest(t).QueryRow("SELECT sqlite_version()").Scan(&bundled); err != nil {
		t.Fatal(err)
	}
	if !versionAtLeast(bundled, recorded) {
		t.Errorf("%s: bundled SQLite %s is older than %s, which wrote the golden DB", driverName, bundled, recorded)
	}
}

func TestConcurrentStartAppliesOnce(t *testing.T) {
	var applied atomic.Int32
	saved := migrations
	migrations = []migration{{1, func(ctx context.Context, tx *sql.Tx) error {
		applied.Add(1)
		return pythonSchema(ctx, tx)
	}}}
	t.Cleanup(func() { migrations = saved })
	path := filepath.Join(t.TempDir(), "atrium.db")
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range cap(errs) {
		wg.Go(func() {
			d, err := Connect(path)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = d.Close() }()
			errs <- Migrate(t.Context(), d, path)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := applied.Load(); n != 1 {
		t.Errorf("migration 0001 applied %d times, want 1", n)
	}
}

func TestMissingFrom(t *testing.T) {
	ctx := t.Context()
	want, err := referenceSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"fts5", "vec0", "view"} {
		found := false
		for _, k := range want.objects {
			found = found || k == kind
		}
		if !found {
			t.Errorf("reference schema has no %s", kind)
		}
	}
	mem := Open(":memory:")
	defer func() { _ = mem.Close() }()
	conn, err := mem.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := initSchema(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `DROP VIEW filed_messages; DROP INDEX messages_gm; ALTER TABLE drafts DROP COLUMN sent_msgid;
		DROP TABLE notion_vec; CREATE TABLE notion_vec (x); `); err != nil {
		t.Fatal(err)
	}
	got, err := readSchema(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	missing := strings.Join(want.missingFrom(got), "; ")
	for _, m := range []string{"view filed_messages", "index messages_gm on messages", "column drafts.sent_msgid", "vec0 notion_vec", "column filed_messages.id"} {
		if !strings.Contains(missing, m) {
			t.Errorf("missing %q not reported in %q", m, missing)
		}
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE "Extra" (y); CREATE INDEX extra_y ON "Extra" (y)`); err != nil {
		t.Fatal(err)
	}
	if got, err = readSchema(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if again := strings.Join(want.missingFrom(got), "; "); again != missing {
		t.Errorf("an extra table changed the report:\n%s\n%s", missing, again)
	}
}
