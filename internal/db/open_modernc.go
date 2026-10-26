//go:build modernc

package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
	_ "modernc.org/sqlite/vec"
)

const driverName = "modernc"

func openDriver() driver.Driver { return &sqlite.Driver{} }

func dsn(path string, immediate bool, key Key) (string, error) {
	if !key.IsZero() {
		return "", ErrNoCipher
	}
	if immediate {
		return path + "?_txlock=immediate", nil
	}
	return path, nil
}

func isBusy(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code()&0xff == sqlite3.SQLITE_BUSY
}

func IsConstraint(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}

func backupConns(ctx context.Context, src, _ *sql.DB, dest string) (err error) {
	conn, err := src.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	if _, err := conn.ExecContext(ctx, "PRAGMA query_only = OFF"); err != nil {
		return err
	}
	defer func() {
		if _, rerr := conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA query_only = ON"); rerr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, rerr)
		}
	}()
	_, err = conn.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(dest, "'", "''")+"'")
	return err
}
