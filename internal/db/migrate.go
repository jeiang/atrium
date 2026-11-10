package db

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
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
	{1, func(ctx context.Context, tx *sql.Tx) error { return initSchema(ctx, tx) }},
	{2, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "ALTER TABLE threads ADD COLUMN tainted_at REAL")
		return err
	}},
	{3, func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range []string{
			"ALTER TABLE packages ADD COLUMN aggregator TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE packages ADD COLUMN tracker_id TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE packages ADD COLUMN tracker_registered_ts REAL NOT NULL DEFAULT 0",
			"ALTER TABLE packages ADD COLUMN last_sync_ts REAL NOT NULL DEFAULT 0",
			"ALTER TABLE packages ADD COLUMN last_event_hash TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE packages ADD COLUMN last_event TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE packages ADD COLUMN last_event_ts REAL NOT NULL DEFAULT 0",
			"ALTER TABLE packages ADD COLUMN notified_status TEXT NOT NULL DEFAULT ''",
			`CREATE TABLE IF NOT EXISTS package_events (
				id INTEGER PRIMARY KEY,
				package_id INTEGER NOT NULL,
				tracker_id TEXT NOT NULL DEFAULT '',
				event_key TEXT NOT NULL,
				event_ts REAL NOT NULL DEFAULT 0,
				status TEXT NOT NULL DEFAULT '',
				status_code TEXT NOT NULL DEFAULT '',
				status_milestone TEXT NOT NULL DEFAULT '',
				description TEXT NOT NULL DEFAULT '',
				location TEXT NOT NULL DEFAULT '',
				created_ts REAL NOT NULL,
				UNIQUE (package_id, event_key)
			)`,
			"CREATE INDEX IF NOT EXISTS package_events_package ON package_events (package_id, event_ts)",
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}},
	{4, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS push_subscriptions (
			id INTEGER PRIMARY KEY,
			endpoint TEXT NOT NULL UNIQUE,
			p256dh TEXT NOT NULL,
			auth TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			created_ts REAL NOT NULL,
			last_ok_ts REAL,
			failure_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT
		)`)
		return err
	}},
	{5, func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range []string{
			"ALTER TABLE threads ADD COLUMN taint_private INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE threads ADD COLUMN taint_web INTEGER NOT NULL DEFAULT 0",
			"UPDATE threads SET taint_private = 1, taint_web = 1 WHERE tainted_at IS NOT NULL",
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}},
	{6, func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range []string{
			"ALTER TABLE threads ADD COLUMN marks INTEGER NOT NULL DEFAULT 0",
			"UPDATE threads SET marks = taint_web | (taint_private * 14) WHERE taint_private = 1 OR taint_web = 1",
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}},
	{7, func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range []string{
			"ALTER TABLE projects ADD COLUMN private INTEGER NOT NULL DEFAULT 0",
			`CREATE TABLE IF NOT EXISTS kb_records (
				id INTEGER PRIMARY KEY,
				title TEXT NOT NULL,
				aliases TEXT NOT NULL DEFAULT '[]',
				body TEXT NOT NULL DEFAULT '',
				scope TEXT NOT NULL CHECK (scope IN ('work', 'personal', 'ops')),
				project_id INTEGER,
				private INTEGER NOT NULL DEFAULT 0,
				marks INTEGER NOT NULL DEFAULT 0,
				writer TEXT NOT NULL CHECK (writer IN ('operator', 'chat', 'dreaming', 'worker', 'import')),
				source_thread_id INTEGER,
				source_seq INTEGER,
				source_run_id INTEGER,
				origin TEXT UNIQUE,
				pinned INTEGER NOT NULL DEFAULT 0,
				confirmed_at REAL,
				retracted_at REAL,
				expires_at REAL,
				embedded INTEGER NOT NULL DEFAULT 0,
				created_at REAL NOT NULL,
				updated_at REAL NOT NULL
			)`,
			"CREATE INDEX IF NOT EXISTS kb_records_run ON kb_records (source_run_id) WHERE source_run_id IS NOT NULL",
			"CREATE INDEX IF NOT EXISTS kb_records_project ON kb_records (project_id) WHERE project_id IS NOT NULL",
			"CREATE INDEX IF NOT EXISTS kb_records_pending ON kb_records (embedded) WHERE embedded = 0",
			"CREATE VIRTUAL TABLE IF NOT EXISTS kb_fts USING fts5(title, aliases, body)",
			`CREATE TRIGGER IF NOT EXISTS kb_fts_insert AFTER INSERT ON kb_records BEGIN
				INSERT INTO kb_fts (rowid, title, aliases, body) VALUES (new.id, new.title, new.aliases, new.body);
			END`,
			`CREATE TRIGGER IF NOT EXISTS kb_fts_update AFTER UPDATE OF title, aliases, body ON kb_records BEGIN
				DELETE FROM kb_fts WHERE rowid = old.id;
				INSERT INTO kb_fts (rowid, title, aliases, body) VALUES (new.id, new.title, new.aliases, new.body);
			END`,
			`CREATE TRIGGER IF NOT EXISTS kb_fts_delete AFTER DELETE ON kb_records BEGIN
				DELETE FROM kb_fts WHERE rowid = old.id;
			END`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, "SELECT generation, dimension FROM embed_index ORDER BY generation")
		if err != nil {
			return err
		}
		gens, err := Collect(rows, func(r *sql.Rows) (struct{ gen, dim int64 }, error) {
			var g struct{ gen, dim int64 }
			err := r.Scan(&g.gen, &g.dim)
			return g, err
		})
		if err != nil {
			return err
		}
		for _, g := range gens {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(
				"CREATE VIRTUAL TABLE IF NOT EXISTS embed_kb_%d USING vec0(embedding FLOAT[%d] DISTANCE_METRIC=cosine)", g.gen, g.dim)); err != nil {
				return err
			}
		}
		return nil
	}},
	{8, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS export_pages (
			target TEXT NOT NULL CHECK (target IN ('notion', 'obsidian')),
			record_id INTEGER NOT NULL,
			remote_id TEXT NOT NULL,
			hash TEXT NOT NULL,
			updated_at REAL NOT NULL,
			PRIMARY KEY (target, record_id)
		)`)
		return err
	}},
}

type ModuleMigration struct {
	Version int
	Apply   func(ctx context.Context, tx *sql.Tx) error
}

var moduleMigrations = map[string][]ModuleMigration{}

func RegisterModuleMigrations(module string, ms ...ModuleMigration) {
	have := moduleMigrations[module]
	for _, m := range ms {
		if m.Apply == nil || m.Version < 1 || (len(have) > 0 && m.Version <= have[len(have)-1].Version) {
			panic(fmt.Sprintf("db: module %s migration %d is not a new increasing version", module, m.Version))
		}
		have = append(have, m)
	}
	moduleMigrations[module] = have
}

type FutureModuleSchemaError struct {
	Module   string
	Versions []int
	Known    int
}

func (e *FutureModuleSchemaError) Error() string {
	return fmt.Sprintf("db: module_migrations holds %s migration %v that this atrium does not know (newest known: %d); "+
		"the database was migrated by a build with a newer %s module: run that build or restore a backup taken before the upgrade; nothing was changed",
		e.Module, e.Versions, e.Known, e.Module)
}

type FutureSchemaError struct {
	Versions []int
	Known    int
}

func (e *FutureSchemaError) Error() string {
	versions := make([]string, len(e.Versions))
	for i, v := range e.Versions {
		versions[i] = fmt.Sprintf("%04d", v)
	}
	return fmt.Sprintf("db: schema_migrations holds migration %s that this atrium does not know (newest known: %04d); "+
		"the database was migrated by a newer atrium: run that release or restore a backup taken before the upgrade; nothing was changed",
		strings.Join(versions, ", "), e.Known)
}

func unknownVersions(applied map[int]bool) []int {
	var out []int
	for v := range applied {
		if !slices.ContainsFunc(migrations, func(m migration) bool { return m.version == v }) {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

func refuseFuture(applied map[int]bool) error {
	if future := unknownVersions(applied); len(future) > 0 {
		return &FutureSchemaError{Versions: future, Known: migrations[len(migrations)-1].version}
	}
	return nil
}

func refuseFutureModules(applied map[string]map[int]bool) error {
	for _, name := range slices.Sorted(maps.Keys(applied)) {
		known := moduleMigrations[name]
		if len(known) == 0 {
			continue
		}
		var future []int
		for v := range applied[name] {
			if !slices.ContainsFunc(known, func(m ModuleMigration) bool { return m.Version == v }) {
				future = append(future, v)
			}
		}
		if len(future) > 0 {
			sort.Ints(future)
			return &FutureModuleSchemaError{Module: name, Versions: future, Known: known[len(known)-1].Version}
		}
	}
	return nil
}

func refuseAllFuture(ctx context.Context, q Querier) (map[int]bool, map[string]map[int]bool, error) {
	applied, err := appliedVersions(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	if err := refuseFuture(applied); err != nil {
		return nil, nil, err
	}
	mods, err := appliedModuleVersions(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	return applied, mods, refuseFutureModules(mods)
}

func RefuseFutureSchema(ctx context.Context, q Querier) error {
	_, _, err := refuseAllFuture(ctx, q)
	return err
}

const schemaMigrationsDDL = "CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at REAL NOT NULL)"

func Migrate(ctx context.Context, d *DB, path string) error {
	applied, mods, err := refuseAllFuture(ctx, d.Read)
	if err != nil {
		return err
	}
	if pending(applied, mods) {
		if err := applyPending(ctx, d, path); err != nil {
			return err
		}
	}
	if err := d.WithTx(ctx, func(tx *sql.Tx) error { return seed(ctx, tx, time.Now()) }); err != nil {
		return err
	}
	return Ready(ctx, d)
}

func pending(applied map[int]bool, mods map[string]map[int]bool) bool {
	for _, m := range migrations {
		if !applied[m.version] {
			return true
		}
	}
	for name, ms := range moduleMigrations {
		for _, m := range ms {
			if !mods[name][m.Version] {
				return true
			}
		}
	}
	return false
}

func appliedModuleVersions(ctx context.Context, q Querier) (map[string]map[int]bool, error) {
	var n int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'module_migrations'").Scan(&n); err != nil {
		return nil, err
	}
	out := map[string]map[int]bool{}
	if n == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, "SELECT module, version FROM module_migrations")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		var v int
		if err := rows.Scan(&name, &v); err != nil {
			return nil, err
		}
		if out[name] == nil {
			out[name] = map[int]bool{}
		}
		out[name][v] = true
	}
	return out, rows.Err()
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
	lock, err := Exclusive(ctx, path, 0)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if _, _, err := refuseAllFuture(ctx, d.Read); err != nil {
		return err
	}
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
	for _, name := range slices.Sorted(maps.Keys(moduleMigrations)) {
		for _, m := range moduleMigrations[name] {
			err := d.WithTx(ctx, func(tx *sql.Tx) error {
				var done int
				if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM module_migrations WHERE module = ? AND version = ?", name, m.Version).Scan(&done); err != nil {
					return err
				}
				if done > 0 {
					return nil
				}
				if err := m.Apply(ctx, tx); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, "INSERT INTO module_migrations (module, version, applied_at) VALUES (?, ?, ?)",
					name, m.Version, float64(time.Now().UnixNano())/1e9)
				return err
			})
			if err != nil {
				return fmt.Errorf("db: module %s migration %d: %w", name, m.Version, err)
			}
		}
	}
	return nil
}

const KeyHistoryDDL = "CREATE TABLE IF NOT EXISTS db_key_history (" +
	"id INTEGER PRIMARY KEY AUTOINCREMENT, key_id TEXT NOT NULL, replaced_by TEXT NOT NULL, retired_at REAL NOT NULL)"
