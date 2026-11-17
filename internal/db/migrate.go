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
	{1, func(ctx context.Context, tx *sql.Tx) error { return initSchema(ctx, tx) }},
	{2, dropUnreadTables},
	{3, addUsageCounters},
	{4, addRecordLinks},
	{5, dropOpsFindings},
	{6, addLibraryRequests},
	{7, narrowConnectionReadTrigger},
}

const (
	unreadTablesDDL = `
DROP TABLE nightly_observations;
DROP TABLE agent_profile;
DROP TABLE notion_pages;
DROP TABLE notion_sources;
DROP TABLE notion_state;
DROP TABLE notion_fts;
DROP TABLE wiki_pages;
DROP TABLE action_links;
DROP TABLE rebalance_candidates;
DROP TABLE pass_runs;
DROP TABLE pass_run_alerts;
DROP TABLE snoozes;
`
	notionVectorTables = "SELECT name FROM sqlite_master WHERE type = 'table' AND name GLOB 'embed_notion_[0-9]*' AND name NOT GLOB 'embed_notion_*[^0-9]*' AND sql LIKE 'CREATE VIRTUAL TABLE%' ORDER BY name"
	usageCountersDDL   = `
CREATE TABLE usage_counters (day TEXT NOT NULL, surface TEXT NOT NULL, method TEXT NOT NULL, route TEXT NOT NULL, agent INTEGER NOT NULL CHECK (agent IN (0, 1)), count INTEGER NOT NULL CHECK (count >= 0), PRIMARY KEY (day, surface, method, route, agent));
ALTER TABLE auth_bearers ADD COLUMN agent INTEGER NOT NULL DEFAULT 0 CHECK (agent IN (0, 1));
ALTER TABLE auth_sessions ADD COLUMN agent INTEGER NOT NULL DEFAULT 0 CHECK (agent IN (0, 1));
`
	recordLinksDDL = `
CREATE TABLE record_links (id INTEGER PRIMARY KEY, record_id INTEGER NOT NULL REFERENCES kb_records(id) ON DELETE CASCADE, target_kind TEXT NOT NULL, target_id TEXT NOT NULL, relation TEXT NOT NULL, writer TEXT NOT NULL CHECK (writer IN ('operator', 'assistant')), run_id INTEGER, created_at REAL NOT NULL, retracted_at REAL, CHECK ((writer = 'assistant') = (run_id IS NOT NULL)));
CREATE UNIQUE INDEX record_links_live ON record_links (record_id, target_kind, target_id, relation) WHERE retracted_at IS NULL;
CREATE INDEX record_links_target ON record_links (target_kind, target_id);
CREATE TRIGGER IF NOT EXISTS record_links_read_insert AFTER INSERT ON record_links BEGIN INSERT INTO read_revisions(scope,value) VALUES('knowledge',1) ON CONFLICT(scope) DO UPDATE SET value=value+1; END;
CREATE TRIGGER IF NOT EXISTS record_links_read_update AFTER UPDATE ON record_links BEGIN INSERT INTO read_revisions(scope,value) VALUES('knowledge',1) ON CONFLICT(scope) DO UPDATE SET value=value+1; END;
CREATE TRIGGER IF NOT EXISTS record_links_read_delete AFTER DELETE ON record_links BEGIN INSERT INTO read_revisions(scope,value) VALUES('knowledge',1) ON CONFLICT(scope) DO UPDATE SET value=value+1; END;
`
	opsFindingsDDL = `
DROP TABLE ops_findings;
`
	libraryRequestsDDL = `
CREATE TABLE library_requests (id INTEGER PRIMARY KEY, hash TEXT NOT NULL UNIQUE, thread_id INTEGER, proposal_id INTEGER, approved_by TEXT NOT NULL CHECK (approved_by IN ('operator', 'proposal')), approved_at REAL NOT NULL, download TEXT, effect_id INTEGER, filed_by TEXT CHECK (filed_by IN ('job', 'operator')), state TEXT NOT NULL CHECK (state IN ('requested', 'waiting', 'filed', 'failed')), reason TEXT, created_at REAL NOT NULL, updated_at REAL NOT NULL);
CREATE TRIGGER IF NOT EXISTS library_requests_read_insert AFTER INSERT ON library_requests BEGIN INSERT INTO read_revisions(scope,value) VALUES('media',1) ON CONFLICT(scope) DO UPDATE SET value=value+1; END;
CREATE TRIGGER IF NOT EXISTS library_requests_read_update AFTER UPDATE ON library_requests BEGIN INSERT INTO read_revisions(scope,value) VALUES('media',1) ON CONFLICT(scope) DO UPDATE SET value=value+1; END;
CREATE TRIGGER IF NOT EXISTS library_requests_read_delete AFTER DELETE ON library_requests BEGIN INSERT INTO read_revisions(scope,value) VALUES('media',1) ON CONFLICT(scope) DO UPDATE SET value=value+1; END;
`
	connectionReadTriggerDDL = `
DROP TRIGGER connections_read_update;
CREATE TRIGGER connections_read_update AFTER UPDATE ON connections WHEN old.id IS NOT new.id OR old.kind IS NOT new.kind OR old.label IS NOT new.label OR old.config_source IS NOT new.config_source OR old.config_json IS NOT new.config_json OR old.transport_json IS NOT new.transport_json OR old.revision IS NOT new.revision OR old.enabled IS NOT new.enabled OR old.state IS NOT new.state OR old.reason IS NOT new.reason OR old.expires_at IS NOT new.expires_at OR old.reauth_at IS NOT new.reauth_at OR old.failed_test_revision IS NOT new.failed_test_revision OR old.results_json IS NOT new.results_json OR old.created_at IS NOT new.created_at BEGIN INSERT INTO read_revisions(scope,value) VALUES('connections',1) ON CONFLICT(scope) DO UPDATE SET value=value+1; END;
`
)

func addLibraryRequests(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, libraryRequestsDDL)
	return err
}

func narrowConnectionReadTrigger(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, connectionReadTriggerDDL)
	return err
}

func addUsageCounters(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, usageCountersDDL)
	return err
}

func addRecordLinks(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, recordLinksDDL)
	return err
}

func dropUnreadTables(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, notionVectorTables)
	if err != nil {
		return err
	}
	var vectors []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		vectors = append(vectors, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, name := range vectors {
		if _, err := tx.ExecContext(ctx, `DROP TABLE "`+name+`"`); err != nil {
			return fmt.Errorf("drop %s: %w", name, err)
		}
	}
	_, err = tx.ExecContext(ctx, unreadTablesDDL)
	return err
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

func refuseAllFuture(ctx context.Context, q Querier) (map[int]bool, error) {
	applied, err := appliedVersions(ctx, q)
	if err != nil {
		return nil, err
	}
	return applied, refuseFuture(applied)
}

func RefuseFutureSchema(ctx context.Context, q Querier) error {
	_, err := refuseAllFuture(ctx, q)
	return err
}

const (
	ApplicationID       = 0x6174726d
	schemaMigrationsDDL = "CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at REAL NOT NULL)"
)

var ErrNotV3 = errors.New("db: this database has no v3 marker and was created by v2 or older; this release cannot read it: import it into a fresh database with release v3.0.0, the last release that reads v2 data, then upgrade; nothing was changed")

func RequireV3(ctx context.Context, q Querier) error {
	var tables int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return err
	}
	if tables == 0 {
		return nil
	}
	var id int64
	if err := q.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return err
	}
	if id != ApplicationID {
		return ErrNotV3
	}
	return nil
}

func Migrate(ctx context.Context, d *DB, path string) error {
	if err := RequireV3(ctx, d.Read); err != nil {
		return err
	}
	applied, err := refuseAllFuture(ctx, d.Read)
	if err != nil {
		return err
	}
	if pending(applied) {
		if err := applyPending(ctx, d, path); err != nil {
			return err
		}
	}
	return Ready(ctx, d)
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
	lock, err := Exclusive(ctx, path, 0)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if _, err := refuseAllFuture(ctx, d.Read); err != nil {
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
	return nil
}

func RecordRetiredKey(ctx context.Context, tx *sql.Tx, oldID, newID string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO db_key_history (key_id, replaced_by, retired_at) VALUES (?, ?, CAST(strftime('%s','now') AS REAL))", oldID, newID)
	return err
}

func dropOpsFindings(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, opsFindingsDDL)
	return err
}
