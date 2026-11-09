package store

import "fmt"

func Bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

type Flag bool

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
