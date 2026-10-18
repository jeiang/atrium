package dbtest

import (
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

// File connects to atrium.db in a fresh temp directory.
func File(t testing.TB) *db.DB {
	t.Helper()
	return open(t, db.Connect)
}

// FileNoForeignKeys is File without foreign key enforcement, standing in
// for Python's db.connect_memory.
func FileNoForeignKeys(t testing.TB) *db.DB {
	t.Helper()
	return open(t, db.ConnectNoForeignKeys)
}

func open(t testing.TB, connect func(string) (*db.DB, error)) *db.DB {
	t.Helper()
	d, err := connect(filepath.Join(t.TempDir(), "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
