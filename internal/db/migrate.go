package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

type migration struct {
	version int
	apply   func(ctx context.Context, tx *sql.Tx) error
}

var migrations = []migration{
	{1, pythonSchema},
}

const schemaMigrationsDDL = "CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at REAL NOT NULL)"

// Migrate applies the pending schema migrations of the database at path,
// then seeds the built-in rows. Call it once at process start. Only a
// pending migration takes the atrium.db.lock flock; each one is applied in
// its own transaction and recorded in schema_migrations.
func Migrate(ctx context.Context, d *DB, path string) error {
	applied, err := appliedVersions(ctx, d.Read)
	if err != nil {
		return err
	}
	if pending(applied) {
		if err := applyPending(ctx, d, path); err != nil {
			return err
		}
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error { return seed(ctx, tx, time.Now()) })
}

func pending(applied map[int]bool) bool {
	for _, m := range migrations {
		if !applied[m.version] {
			return true
		}
	}
	return false
}

func appliedVersions(ctx context.Context, q Querier) (map[int]bool, error) {
	var n int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'").Scan(&n); err != nil {
		return nil, err
	}
	out := map[int]bool{}
	if n == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

func applyPending(ctx context.Context, d *DB, path string) error {
	lock, err := Exclusive(path)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	for _, m := range migrations {
		err := d.WithTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, schemaMigrationsDDL); err != nil {
				return err
			}
			var done int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.version).Scan(&done); err != nil {
				return err
			}
			if done > 0 {
				return nil
			}
			if err := m.apply(ctx, tx); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
				m.version, float64(time.Now().UnixNano())/1e9)
			return err
		})
		if err != nil {
			return fmt.Errorf("db: migration %04d: %w", m.version, err)
		}
	}
	return nil
}

// pythonSchema is migration 0001: Python's schema pass, which creates the
// frozen schema on an empty database and brings a Python-made one, legacy
// shapes included, to it; then the result must hold every table, column,
// index, FTS5 and vec0 table a fresh database has.
func pythonSchema(ctx context.Context, tx *sql.Tx) error {
	if err := initSchema(ctx, tx); err != nil {
		return err
	}
	want, err := referenceSchema(ctx)
	if err != nil {
		return err
	}
	got, err := readSchema(ctx, tx)
	if err != nil {
		return err
	}
	if missing := want.missingFrom(got); len(missing) > 0 {
		return fmt.Errorf("schema lacks %s", strings.Join(missing, ", "))
	}
	return nil
}

type schemaShape struct {
	objects map[string]string
	columns map[string][]string
	indexes map[string][]string
}

func referenceSchema(ctx context.Context) (schemaShape, error) {
	mem := Open(":memory:")
	defer func() { _ = mem.Close() }()
	conn, err := mem.Conn(ctx)
	if err != nil {
		return schemaShape{}, err
	}
	defer func() { _ = conn.Close() }()
	if err := initSchema(ctx, conn); err != nil {
		return schemaShape{}, err
	}
	return readSchema(ctx, conn)
}

func objectKind(typ, sqlText string) string {
	upper := strings.ToUpper(sqlText)
	switch {
	case typ != "table" || !strings.HasPrefix(upper, "CREATE VIRTUAL TABLE"):
		return typ
	case strings.Contains(upper, "USING FTS5"):
		return "fts5"
	case strings.Contains(upper, "USING VEC0"):
		return "vec0"
	}
	return "virtual"
}

func unquote(name string) string {
	return strings.ToLower(strings.Trim(name, "\"`[]"))
}

func readSchema(ctx context.Context, q Querier) (schemaShape, error) {
	s := schemaShape{objects: map[string]string{}, columns: map[string][]string{}, indexes: map[string][]string{}}
	rows, err := q.QueryContext(ctx, "SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'")
	if err != nil {
		return s, err
	}
	var tables []string
	for rows.Next() {
		var typ, name, text string
		if err := rows.Scan(&typ, &name, &text); err != nil {
			_ = rows.Close()
			return s, err
		}
		s.objects[unquote(name)] = objectKind(typ, text)
		if typ == "table" || typ == "view" {
			tables = append(tables, name)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return s, err
	}
	for _, t := range tables {
		if shadowOf(s.objects, unquote(t)) {
			delete(s.objects, unquote(t))
			continue
		}
		cols, err := tableInfo(ctx, q, t)
		if err != nil {
			return s, err
		}
		for _, c := range cols {
			s.columns[unquote(t)] = append(s.columns[unquote(t)], unquote(c.name))
		}
		names, err := indexNames(ctx, q, t)
		if err != nil {
			return s, err
		}
		s.indexes[unquote(t)] = names
	}
	return s, nil
}

// shadowOf reports a virtual table's own storage table, whose set differs
// between sqlite-vec releases.
func shadowOf(objects map[string]string, name string) bool {
	for vt, kind := range objects {
		if (kind == "fts5" || kind == "vec0" || kind == "virtual") && strings.HasPrefix(name, vt+"_") {
			return true
		}
	}
	return false
}

func indexNames(ctx context.Context, q Querier, table string) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM pragma_index_list(?) WHERE origin = 'c'", table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, unquote(name))
	}
	return out, rows.Err()
}

func (want schemaShape) missingFrom(got schemaShape) []string {
	var missing []string
	for name, kind := range want.objects {
		if got.objects[name] != kind {
			missing = append(missing, fmt.Sprintf("%s %s", kind, name))
		}
	}
	for table, cols := range want.columns {
		for _, c := range cols {
			if !slices.Contains(got.columns[table], c) {
				missing = append(missing, fmt.Sprintf("column %s.%s", table, c))
			}
		}
	}
	for table, idx := range want.indexes {
		for _, i := range idx {
			if !slices.Contains(got.indexes[table], i) {
				missing = append(missing, fmt.Sprintf("index %s on %s", i, table))
			}
		}
	}
	sort.Strings(missing)
	return missing
}
