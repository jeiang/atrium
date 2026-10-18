//go:build !modernc

package db

import (
	"database/sql/driver"

	sqlitevec "github.com/jeiang/atrium/third_party/sqlite-vec"
	"github.com/mattn/go-sqlite3"
)

const driverName = "mattn"

func init() { sqlitevec.Auto() }

func openDriver() driver.Driver { return &sqlite3.SQLiteDriver{} }
