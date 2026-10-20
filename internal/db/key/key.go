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
	resolved, err := reg.Resolve(context.Background(), nil, settingKey, 0)
	if err != nil {
		return db.Key{}, err
	}
	if resolved.Pinned() {
		k, err := db.ParseKey(resolved.Value)
		if err != nil {
			return db.Key{}, fmt.Errorf("db.key (%s): %w", resolved.PinSource, err)
		}
		return k, nil
	}
	dir, _ := env("CREDENTIALS_DIRECTORY")
	if dir == "" {
		return db.Key{}, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, db.KeyCredential))
	if errors.Is(err, fs.ErrNotExist) {
		return db.Key{}, nil
	}
	if err != nil {
		return db.Key{}, fmt.Errorf("%s credential: %w", db.KeyCredential, err)
	}
	k, err := db.ParseKey(strings.TrimSpace(string(raw)))
	if err != nil {
		return db.Key{}, fmt.Errorf("%s credential: %w", db.KeyCredential, err)
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
