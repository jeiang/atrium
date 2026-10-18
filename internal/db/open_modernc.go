//go:build modernc

package db

import (
	"database/sql/driver"
	"errors"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
	_ "modernc.org/sqlite/vec"
)

const driverName = "modernc"

func openDriver() driver.Driver { return &sqlite.Driver{} }

func isBusy(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code()&0xff == sqlite3.SQLITE_BUSY
}
