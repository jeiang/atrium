package db

import (
	"context"
	"database/sql"
	"errors"
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
	{1, pythonSchema},
	{2, usageIndexes},
	{3, secretsTable},
	{4, settingsAudit},
	{5, keyHistory},
	{6, writeResolutions},
	{7, threadModel},
	{8, authSessions},
	{9, loginLinks},
	{10, moduleMigrationsTable},
	{11, passRuns},
	{12, ntfyChannels},
	{13, endpointProbes},
	{14, integrationHealth},
	{15, mailAccounts},
	{16, embedIndex},
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

func usageIndexes(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		"CREATE INDEX IF NOT EXISTS usage_thread ON usage (thread_id)",
		"CREATE INDEX IF NOT EXISTS usage_run ON usage (run_id)",
		"CREATE INDEX IF NOT EXISTS runs_parent ON runs (parent_run_id)",
		"CREATE INDEX IF NOT EXISTS tool_results_run ON tool_results (run_id)",
		"CREATE INDEX IF NOT EXISTS threads_parent_thread ON threads (parent_thread_id)",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func secretsTable(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS secrets ("+
		"name TEXT PRIMARY KEY, value TEXT NOT NULL, set_at REAL NOT NULL, set_by TEXT NOT NULL)")
	return err
}

func settingsAudit(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		"CREATE TABLE IF NOT EXISTS settings_audit (" +
			"id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL NOT NULL, key TEXT NOT NULL, thread_id INTEGER, " +
			"old_value TEXT, new_value TEXT, secret INTEGER NOT NULL CHECK (secret IN (0, 1)), " +
			"actor TEXT NOT NULL, source TEXT NOT NULL CHECK (source IN ('cli', 'ui', 'migration')), " +
			"CHECK (secret = 0 OR (old_value IS NULL AND new_value = 'changed')))",
		"CREATE TRIGGER IF NOT EXISTS settings_audit_no_update BEFORE UPDATE ON settings_audit " +
			"BEGIN SELECT RAISE(ABORT, 'settings_audit is append-only'); END",
		"CREATE TRIGGER IF NOT EXISTS settings_audit_no_delete BEFORE DELETE ON settings_audit " +
			"BEGIN SELECT RAISE(ABORT, 'settings_audit is append-only'); END",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

const KeyHistoryDDL = "CREATE TABLE IF NOT EXISTS db_key_history (" +
	"id INTEGER PRIMARY KEY AUTOINCREMENT, key_id TEXT NOT NULL, replaced_by TEXT NOT NULL, retired_at REAL NOT NULL)"

func keyHistory(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, KeyHistoryDDL)
	return err
}

func writeResolutions(ctx context.Context, tx *sql.Tx) error {
	if _, err := addColumn(ctx, tx, "auto_actions", column{"undo_started_at", "REAL"}); err != nil {
		return err
	}
	for _, stmt := range []string{
		"CREATE TABLE IF NOT EXISTS write_resolutions (" +
			"id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL NOT NULL, actor TEXT NOT NULL, " +
			"source TEXT NOT NULL CHECK (source IN ('cli', 'ui')), " +
			"kind TEXT NOT NULL CHECK (kind IN ('proposal_action', 'auto_undo')), " +
			"row_id INTEGER NOT NULL, proposal_id INTEGER, outcome TEXT NOT NULL, prior_error TEXT NOT NULL)",
		"CREATE TRIGGER IF NOT EXISTS write_resolutions_no_update BEFORE UPDATE ON write_resolutions " +
			"BEGIN SELECT RAISE(ABORT, 'write_resolutions is append-only'); END",
		"CREATE TRIGGER IF NOT EXISTS write_resolutions_no_delete BEFORE DELETE ON write_resolutions " +
			"BEGIN SELECT RAISE(ABORT, 'write_resolutions is append-only'); END",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func threadModel(ctx context.Context, tx *sql.Tx) error {
	_, err := addColumn(ctx, tx, "threads", column{"model", "TEXT NOT NULL DEFAULT 'claude-haiku-5-5'"})
	return err
}

func authSessions(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		"CREATE TABLE IF NOT EXISTS auth_sessions (" +
			"id INTEGER PRIMARY KEY AUTOINCREMENT, token_hash TEXT NOT NULL UNIQUE, subject TEXT NOT NULL, " +
			"email TEXT NOT NULL DEFAULT '', name TEXT NOT NULL DEFAULT '', admin INTEGER NOT NULL CHECK (admin IN (0, 1)), " +
			"created_at REAL NOT NULL, last_seen_at REAL NOT NULL, idle_expires_at REAL NOT NULL, " +
			"absolute_expires_at REAL NOT NULL, rechecked_at REAL NOT NULL, " +
			"access_token TEXT NOT NULL DEFAULT '', access_expires_at REAL, refresh_token TEXT NOT NULL DEFAULT '', " +
			"email_verified INTEGER NOT NULL DEFAULT 0 CHECK (email_verified IN (0, 1)), hosted_domain TEXT NOT NULL DEFAULT '')",
		"CREATE INDEX IF NOT EXISTS auth_sessions_absolute ON auth_sessions (absolute_expires_at)",
		"CREATE TABLE IF NOT EXISTS auth_logins (" +
			"state_hash TEXT PRIMARY KEY, binding_hash TEXT NOT NULL, nonce TEXT NOT NULL, verifier TEXT NOT NULL, " +
			"return_to TEXT NOT NULL, created_at REAL NOT NULL)",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func loginLinks(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS auth_login_links ("+
		"id INTEGER PRIMARY KEY AUTOINCREMENT, nonce_hash TEXT NOT NULL UNIQUE, issued_at REAL NOT NULL, "+
		"expires_at REAL NOT NULL, used_at REAL)"); err != nil {
		return err
	}
	_, err := addColumn(ctx, tx, "auth_sessions", column{"origin", "TEXT NOT NULL DEFAULT 'oidc' CHECK (origin IN ('oidc', 'link'))"})
	return err
}

func moduleMigrationsTable(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS module_migrations ("+
		"module TEXT NOT NULL, version INTEGER NOT NULL, applied_at REAL NOT NULL, PRIMARY KEY (module, version))")
	return err
}

func passRuns(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		"CREATE TABLE IF NOT EXISTS pass_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, job TEXT NOT NULL, " +
			"started_at REAL NOT NULL, finished_at REAL NOT NULL, outcome TEXT NOT NULL CHECK (outcome IN ('ok', 'failed')), " +
			"message TEXT NOT NULL DEFAULT '')",
		"CREATE INDEX IF NOT EXISTS pass_runs_job ON pass_runs (job, id)",
		"CREATE TABLE IF NOT EXISTS pass_run_alerts (job TEXT PRIMARY KEY, notified_at REAL NOT NULL)",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func ntfyChannels(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS ntfy_channels (name TEXT PRIMARY KEY, topic TEXT NOT NULL, "+
		"privacy TEXT NOT NULL DEFAULT 'unknown' CHECK (privacy IN ('unknown', 'private', 'public')), "+
		"content TEXT NOT NULL DEFAULT 'auto' CHECK (content IN ('auto', 'full', 'title')), probed_at REAL, "+
		"failures INTEGER NOT NULL DEFAULT 0, last_error TEXT, last_ok_at REAL, created_at REAL NOT NULL)"); err != nil {
		return err
	}
	for _, c := range []column{{"channel", "TEXT"}, {"click", "TEXT"}, {"priority", "INTEGER"}} {
		if _, err := addColumn(ctx, tx, "notifications", c); err != nil {
			return err
		}
	}
	return nil
}

func endpointProbes(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS endpoint_probes (kind TEXT PRIMARY KEY CHECK (kind IN ('chat', 'embed')), "+
		"fingerprint TEXT NOT NULL, ok INTEGER NOT NULL, reason TEXT NOT NULL DEFAULT '', dimension INTEGER, "+
		"models TEXT NOT NULL DEFAULT '[]', probed_at REAL NOT NULL)")
	return err
}

func integrationHealth(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS integration_health (name TEXT PRIMARY KEY, "+
		"state TEXT NOT NULL CHECK (state IN ('ok', 'failing', 'expiring', 'needs_reauth')), reason TEXT NOT NULL DEFAULT '', "+
		"checked_at REAL NOT NULL, expires_at REAL)")
	return err
}

func mailAccounts(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS mail_accounts (id TEXT PRIMARY KEY, label TEXT NOT NULL, "+
		"preset TEXT NOT NULL CHECK (preset IN ('icloud', 'gmail')), login TEXT NOT NULL, "+
		"enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)), created_at REAL NOT NULL)")
	return err
}

func embedIndex(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		"CREATE TABLE IF NOT EXISTS embed_index (generation INTEGER PRIMARY KEY CHECK (generation >= 0), " +
			"state TEXT NOT NULL UNIQUE CHECK (state IN ('active', 'building')), url TEXT NOT NULL DEFAULT '', " +
			"model TEXT NOT NULL DEFAULT '', dimension INTEGER NOT NULL CHECK (dimension > 0), " +
			"done INTEGER NOT NULL DEFAULT 0, total INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', " +
			"started_at REAL, finished_at REAL)",
		"INSERT OR IGNORE INTO embed_index (generation, state, dimension) VALUES (0, 'active', 768)",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
