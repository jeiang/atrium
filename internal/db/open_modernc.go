//go:build modernc

package db

import (
	"database/sql/driver"

	"modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

const driverName = "modernc"

func openDriver() driver.Driver { return &sqlite.Driver{} }
