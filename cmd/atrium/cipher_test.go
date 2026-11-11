//go:build !modernc

package main

import (
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func requireCipher(t *testing.T) {
	t.Helper()
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
}
