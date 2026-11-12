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

var ErrNotV3 = errors.New("db: this database has no v3 marker and was created by v2 or older; import it with `atrium db import-v2` into a fresh database; nothing was changed")

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
		if err := probeServe(ctx, d, path); err != nil {
			return err
		}
		if err := applyPending(ctx, d, path); err != nil {
			return err
		}
	}
	if err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := seed(ctx, tx, time.Now()); err != nil {
			return err
		}
		return retire(ctx, tx)
	}); err != nil {
		return err
	}
	return Ready(ctx, d)
}

func probeServe(ctx context.Context, d *DB, path string) error {
	if !d.deferToServe {
		return nil
	}
	l, err := TryServeLock(ctx, path)
	var running *AlreadyRunning
	if errors.As(err, &running) {
		return fmt.Errorf("db: %s needs schema migrations, which only the full atrium serve may apply while it runs: %w", running.Path, err)
	}
	if err != nil {
		return err
	}
	return l.Close()
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

const KeyHistoryDDL = "CREATE TABLE IF NOT EXISTS db_key_history (" +
	"id INTEGER PRIMARY KEY AUTOINCREMENT, key_id TEXT NOT NULL, replaced_by TEXT NOT NULL, retired_at REAL NOT NULL)"
