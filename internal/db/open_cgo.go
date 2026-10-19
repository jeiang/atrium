//go:build !modernc

package db

import (
	"database/sql/driver"
	"errors"

	sqlitevec "github.com/jeiang/atrium/third_party/sqlite-vec"
	"github.com/mattn/go-sqlite3"
)

const driverName = "mattn"

func init() { sqlitevec.Auto() }

func openDriver() driver.Driver { return &sqlite3.SQLiteDriver{} }

func isBusy(err error) bool {
	var e sqlite3.Error
	return errors.As(err, &e) && e.Code == sqlite3.ErrBusy
}

// IsConstraint reports a constraint violation, Python's sqlite3.IntegrityError.
func IsConstraint(err error) bool {
	var e sqlite3.Error
	return errors.As(err, &e) && e.Code == sqlite3.ErrConstraint
}
