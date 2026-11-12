//go:build !modernc

package main

import (
	"testing"
)

func requireCipher(t *testing.T) {
	t.Helper()
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
}
