package dbtest

import (
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func Schema(t testing.TB) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atrium.db")
	return migrate(t, path)
}

func migrate(t testing.TB, path string) *db.DB {
	t.Helper()
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(t.Context(), d, path); err != nil {
		t.Fatal(err)
	}
	return d
}
