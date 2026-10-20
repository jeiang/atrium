package db

import (
	"encoding/hex"
	"strings"
)

type KeyError string

func (e KeyError) Error() string { return string(e) }

const (
	ErrNoCipher   KeyError = "db: this build has no SQLCipher engine"
	ErrInvalidKey KeyError = "db: key must be 64 hex digits"
)

type Key struct{ hex string }

func ParseKey(s string) (Key, error) {
	if len(s) != 64 {
		return Key{}, ErrInvalidKey
	}
	if _, err := hex.DecodeString(s); err != nil {
		return Key{}, ErrInvalidKey
	}
	return Key{hex: strings.ToLower(s)}, nil
}

func (k Key) IsZero() bool { return k.hex == "" }

func (Key) String() string { return "[redacted]" }

type options struct{ key Key }

type Option func(*options)

func WithKey(k Key) Option { return func(o *options) { o.key = k } }

func applyOptions(opts []Option) options {
	var o options
	for _, f := range opts {
		f(&o)
	}
	return o
}

func escapeURIPath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(path)
}
