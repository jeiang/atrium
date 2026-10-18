package dbtest

import (
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

// File connects to atrium.db in a fresh temp directory.
func File(t testing.TB) *db.DB {
	t.Helper()
	d, err := db.Connect(filepath.Join(t.TempDir(), "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// Memory opens an in-memory database with foreign keys enforced.
func Memory(t testing.TB) *db.DB {
	t.Helper()
	return memory(t, true)
}

// MemoryNoForeignKeys opens an in-memory database without foreign key
// enforcement, as Python's db.connect_memory.
func MemoryNoForeignKeys(t testing.TB) *db.DB {
	t.Helper()
	return memory(t, false)
}

func memory(t testing.TB, foreignKeys bool) *db.DB {
	t.Helper()
	d, err := db.Memory(foreignKeys)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
