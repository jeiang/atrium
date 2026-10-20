package store

import "fmt"

func Bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

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
