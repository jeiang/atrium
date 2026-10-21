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

func (d *DB) BackupTo(ctx context.Context, dest string, key Key) (err error) {
	dst, err := OpenFile(dest, key)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dst.Close()) }()
	if err := backupConns(ctx, d.Read, dst, dest); err != nil {
		return err
	}
	_, err = dst.ExecContext(ctx, "PRAGMA journal_mode = DELETE")
	return err
}
