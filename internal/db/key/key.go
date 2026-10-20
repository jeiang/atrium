package key

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	subkeyLen = 32
	idLen     = 8
)

func derive(k db.Key, info string, n int) []byte {
	out, err := hkdf.Key(sha256.New, k.Raw(), nil, info, n)
	if err != nil {
		panic(err)
	}
	return out
}

func ID(k db.Key) string { return hex.EncodeToString(derive(k, "atrium/db/key-id/v1", idLen)) }

func Cookie(k db.Key) []byte { return derive(k, "atrium/cookie/v1", subkeyLen) }

func Seal(k db.Key) []byte { return derive(k, "atrium/seal/v1", subkeyLen) }

func Load(env config.Env, reg *settings.Registry) (db.Key, error) {
	resolved, err := reg.Resolve(context.Background(), nil, fileKey, 0)
	if err != nil {
		return db.Key{}, err
	}
	if resolved.Pinned() {
		return read(resolved.Value, db.KeyFileSetting)
	}
	if dir, _ := env("CREDENTIALS_DIRECTORY"); dir != "" {
		k, err := read(filepath.Join(dir, db.KeyCredential), db.KeyCredential+" credential")
		if errors.Is(err, fs.ErrNotExist) {
			return db.Key{}, nil
		}
		return k, err
	}
	return db.Key{}, nil
}

func read(path, name string) (db.Key, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return db.Key{}, fmt.Errorf("db: %s: %w", name, err)
	}
	k, err := db.ParseKey(strings.TrimSpace(string(raw)))
	if err != nil {
		return db.Key{}, fmt.Errorf("%s: %s: %w", name, path, err)
	}
	return k, nil
}

func Configure(env config.Env, reg *settings.Registry) error {
	k, err := Load(env, reg)
	if err != nil {
		return err
	}
	db.SetDefaultKey(k)
	return nil
}
