package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func OpenFile(path string, key Key) (*sql.DB, error) {
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("db: path %q contains '?' or '#'", path)
	}
	s, err := dsn(path, false, key)
	if err != nil {
		return nil, err
	}
	if err := guardFile(path, key); err != nil {
		return nil, err
	}
	w := Open(s)
	w.SetMaxOpenConns(1)
	if err := guardEngine(w, key); err != nil {
		_ = w.Close()
		return nil, err
	}
	return w, nil
}

func (d *DB) BackupTo(ctx context.Context, dest string, key Key) error {
	return Backup(ctx, d.Read, dest, key)
}

var ErrInUse = errors.New("db: another process has the database open; stop the atrium units and timers that use it first")

func HoldExclusive(ctx context.Context, w *sql.DB) error {
	if _, err := w.ExecContext(ctx, "PRAGMA locking_mode = EXCLUSIVE"); err != nil {
		return err
	}
	var mode string
	if err := w.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&mode); err != nil {
		return inUse(err)
	}
	if !strings.EqualFold(mode, "delete") {
		return ErrInUse
	}
	if _, err := w.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		return inUse(err)
	}
	_, err := w.ExecContext(ctx, "COMMIT")
	return inUse(err)
}

func inUse(err error) error {
	if err != nil && isBusy(err) {
		return ErrInUse
	}
	return err
}

func Backup(ctx context.Context, src *sql.DB, dest string, key Key) (err error) {
	dst, err := OpenFile(dest, key)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dst.Close()) }()
	if err := backupConns(ctx, src, dst, dest); err != nil {
		return err
	}
	_, err = dst.ExecContext(ctx, "PRAGMA journal_mode = DELETE")
	return err
}
