package store

import (
	"fmt"
	"time"
)

func Bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

const (
	GoldenNow  = 1792080000.0 + 3*86400
	GoldenZone = "America/Port_of_Spain"
)

func GoldenLocation() (*time.Location, error) { return time.LoadLocation(GoldenZone) }

type Flag bool

func (f Flag) MarshalJSON() ([]byte, error) {
	if f {
		return []byte("1"), nil
	}
	return []byte("0"), nil
}

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
