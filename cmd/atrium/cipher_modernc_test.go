//go:build modernc

package main

import "testing"

func requireCipher(t *testing.T) {
	t.Helper()
	t.Skip("the modernc build has no SQLCipher engine")
}
