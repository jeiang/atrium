//go:build !modernc

package main

import (
	"database/sql"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func TestDatabaseEncryptCopiesAPlaintextDatabaseAndRefusesWhileItIsInUse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_DB_KEY", "")
	t.Setenv("ATRIUM_CONFIG", "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	path := filepath.Join(dir, "atrium.db")
	data, err := os.ReadFile(filepath.Join(golden.Dir(), "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	dbEnv, keyEnv := "ATRIUM_DB="+path, "ATRIUM_DB_KEY="+rightKeyHex

	rng := rand.New(rand.NewPCG(1, 2))
	vector := func() []float32 {
		v := make([]float32, config.EmbedDim)
		for i := range v {
			v[i] = rng.Float32()*2 - 1
		}
		return v
	}
	seed, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3000 {
		if _, err := seed.Write.Exec("INSERT INTO message_vec (rowid, account, embedding) VALUES (?, 'bulk', ?)", 100000+i, db.SerializeFloat32(vector())); err != nil {
			t.Fatal(err)
		}
	}
	query := db.SerializeFloat32(vector())
	nearest := func(d *db.DB) []int64 {
		t.Helper()
		rows, err := d.Read.Query("SELECT rowid FROM message_vec WHERE embedding MATCH ? AND k = 25", query)
		if err != nil {
			t.Fatal(err)
		}
		ids, err := db.Collect(rows, func(r *sql.Rows) (int64, error) {
			var id int64
			return id, r.Scan(&id)
		})
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	want := nearest(seed)
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	counts := func(report string) string {
		var lines []string
		for _, line := range strings.Split(report, "\n") {
			if strings.HasPrefix(line, "fts5 ") || strings.HasPrefix(line, "vec0 ") || strings.HasPrefix(line, "integrity_check") {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	}
	before := runProcess(t, "db check", dbEnv)
	if before.code != cli.ExitOK || !strings.Contains(before.stdout, "vec0 message_vec: 3148") {
		t.Fatalf("db check of the plaintext database: %d %q %q", before.code, before.stdout, before.stderr)
	}
	if got := runProcess(t, "db encrypt", dbEnv); got.code != cli.ExitError || !strings.Contains(got.stderr, "no database key") {
		t.Errorf("encrypt without a key: %d %q", got.code, got.stderr)
	}
	holder, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := holder.Write.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE settings SET value = value WHERE 0"); err != nil {
		t.Fatal(err)
	}
	busy := runProcess(t, "db encrypt", dbEnv, keyEnv)
	_ = tx.Rollback()
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	if busy.code != cli.ExitError || !strings.Contains(busy.stderr, "stop the atrium units") || strings.Contains(busy.stdout+busy.stderr, rightKeyHex) {
		t.Errorf("encrypt while the database is in use: %d %q %q", busy.code, busy.stdout, busy.stderr)
	}
	if still := runProcess(t, "db check", dbEnv); !isPlaintextSQLite(t, path) || still.code != cli.ExitOK || counts(still.stdout) != counts(before.stdout) {
		t.Errorf("the refused encrypt changed the database: %d %q", still.code, still.stdout)
	}
	if left, _ := filepath.Glob(path + ".encrypt*"); len(left) != 0 {
		t.Errorf("the refused encrypt left %v", left)
	}

	done := runProcess(t, "db encrypt", dbEnv, keyEnv)
	if done.code != cli.ExitOK || !strings.HasPrefix(done.stdout, "encrypted "+path) || strings.Contains(done.stdout+done.stderr, rightKeyHex) {
		t.Fatalf("db encrypt: %d %q %q", done.code, done.stdout, done.stderr)
	}
	if isPlaintextSQLite(t, path) {
		t.Fatal("the encrypted database has a plaintext header")
	}
	if left, _ := filepath.Glob(path + ".encrypt*"); len(left) != 0 {
		t.Errorf("encrypt left %v", left)
	}
	if plain := runProcess(t, "db check", dbEnv); plain.code != cli.ExitError || !strings.Contains(plain.stderr, string(db.ErrKeyMissing)) {
		t.Errorf("db check without the key: %d %q", plain.code, plain.stderr)
	}
	after := runProcess(t, "db check", dbEnv, keyEnv)
	if after.code != cli.ExitOK || counts(after.stdout) != counts(before.stdout) {
		t.Errorf("db check with the key: %d %q, want the counts of %q", after.code, after.stdout, before.stdout)
	}
	k, err := db.ParseKey(rightKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := db.Connect(path, db.WithKey(k))
	if err != nil {
		t.Fatal(err)
	}
	got := nearest(encrypted)
	_ = encrypted.Close()
	if !slices.Equal(got, want) {
		t.Errorf("KNN after encrypt = %v, want %v", got, want)
	}
	if again := runProcess(t, "db encrypt", dbEnv, keyEnv); again.code != cli.ExitError || !strings.Contains(again.stderr, "already encrypted") {
		t.Errorf("encrypt of an encrypted database: %d %q", again.code, again.stderr)
	}
}
