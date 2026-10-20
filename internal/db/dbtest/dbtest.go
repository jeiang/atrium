package dbtest

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func File(t testing.TB) *db.DB {
	t.Helper()
	return open(t, db.Connect)
}

func FileNoForeignKeys(t testing.TB) *db.DB {
	t.Helper()
	return open(t, db.ConnectNoForeignKeys)
}

func Schema(t testing.TB) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atrium.db")
	return migrate(t, path)
}

func Copy(t testing.TB, src string) (*db.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atrium.db")
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return migrate(t, path), path
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

func open(t testing.TB, connect func(string) (*db.DB, error)) *db.DB {
	t.Helper()
	d, err := connect(filepath.Join(t.TempDir(), "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
