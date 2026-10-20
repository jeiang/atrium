//go:build !modernc

package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"

	sqlitevec "github.com/jeiang/atrium/third_party/sqlite-vec"
	"github.com/mattn/go-sqlite3"
)

const driverName = "mattn"

func init() {
	sqlitevec.Auto()
	conn, err := openDriver().Open("file::memory:")
	if err != nil {
		panic(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.(driver.ExecerContext).ExecContext(context.Background(), "PRAGMA cipher_log_level = NONE", nil); err != nil {
		panic(err)
	}
}

func openDriver() driver.Driver { return &sqlite3.SQLiteDriver{} }

func dsn(path string, immediate bool, key Key) (string, error) {
	var params []string
	if immediate {
		params = append(params, "_txlock=immediate")
	}
	if !key.IsZero() {
		params = append(params, "key=x%27"+key.hex+"%27")
	}
	s := "file:" + escapeURIPath(path)
	if len(params) > 0 {
		s += "?" + strings.Join(params, "&")
	}
	return s, nil
}

func isBusy(err error) bool {
	var e sqlite3.Error
	return errors.As(err, &e) && e.Code == sqlite3.ErrBusy
}

func IsConstraint(err error) bool {
	var e sqlite3.Error
	return errors.As(err, &e) && e.Code == sqlite3.ErrConstraint
}
