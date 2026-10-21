package db

import (
	"encoding/hex"
	"strings"
)

type KeyError string

func (e KeyError) Error() string { return string(e) }

const (
	KeySetting    = "db.key_file, ATRIUM_DB_KEY or ATRIUM_DB_KEY_FILE"
	KeyCredential = "db_key"
)

const (
	ErrNoCipher    KeyError = "db: a database key is configured but this build has no SQLCipher engine; clear the " + KeySetting + " setting or run a SQLCipher build"
	ErrInvalidKey  KeyError = "db: key must be 64 hex digits"
	ErrPlaintextDB KeyError = "db: a database key is configured but the database file is plaintext; clear the " + KeySetting + " setting or point it at an encrypted database"
	ErrWrongKey    KeyError = "db: the configured database key does not open this database; fix the " + KeySetting + " setting"
	ErrKeyMissing  KeyError = "db: the database file is encrypted but no key is configured; set the " + KeySetting + " setting or the " + KeyCredential + " credential"
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

func (k Key) Raw() []byte {
	raw, _ := hex.DecodeString(k.hex)
	return raw
}

var defaultKey Key

func SetDefaultKey(k Key) { defaultKey = k }

type options struct{ key Key }

type Option func(*options)

func WithKey(k Key) Option { return func(o *options) { o.key = k } }

func applyOptions(opts []Option) options {
	o := options{key: defaultKey}
	for _, f := range opts {
		f(&o)
	}
	return o
}

func escapeURIPath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(path)
}
