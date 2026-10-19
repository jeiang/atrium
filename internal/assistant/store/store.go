// Package store is what the assistant's persistence packages share: Python bool columns, the golden read-path
// clock and the sql.Scanner for flags.
package store

import (
	"fmt"
	"time"
)

// Bit is a Python bool stored as an INTEGER column.
func Bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

// The clock and zone of the golden database's read paths (testdata/golden/db, readpaths.json): the frozen
// app answered them at t0 + 3 days under ATRIUM_TZ=America/Port_of_Spain.
const (
	GoldenNow  = 1792080000.0 + 3*86400
	GoldenZone = "America/Port_of_Spain"
)

// GoldenLocation is the zone GoldenNow's read paths run in.
func GoldenLocation() (*time.Location, error) { return time.LoadLocation(GoldenZone) }

// Flag is a Python bool column: it scans from an INTEGER and encodes in JSON as 0 or 1, like the int
// sqlite3 returns for it.
type Flag bool

// MarshalJSON encodes the flag as 0 or 1.
func (f Flag) MarshalJSON() ([]byte, error) {
	if f {
		return []byte("1"), nil
	}
	return []byte("0"), nil
}

// UnmarshalJSON decodes the 0 or 1 MarshalJSON wrote, and JSON booleans.
func (f *Flag) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "1", "true":
		*f = true
	case "0", "false", "null":
		*f = false
	default:
		return fmt.Errorf("store: %s is not a flag", b)
	}
	return nil
}

// Scan reads an INTEGER column; any non-zero value is true.
func (f *Flag) Scan(src any) error {
	switch v := src.(type) {
	case int64:
		*f = v != 0
	case bool:
		*f = Flag(v)
	case nil:
		*f = false
	default:
		return fmt.Errorf("store: cannot scan %T into a flag", src)
	}
	return nil
}
