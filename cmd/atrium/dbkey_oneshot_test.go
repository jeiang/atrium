//go:build !modernc

package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

var keyedOneShots = [][]string{
	{"settings", "list"},
	{"calendar", "candidates"},
	{"media", "poll"},
	{"effects", "list"},
	{"writes", "list"},
	{"proposals", "list"},
	{"agent", "subagents", "list"},
	{"connections", "list"},
}

func TestKeyedOneShotCommandsOpenOnlyTheEncryptedDatabase(t *testing.T) {
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	path := filepath.Join(dir, "atrium.db")
	for _, args := range keyedOneShots {
		if ran := runApp(t, args...); ran.code != cli.ExitOK {
			t.Errorf("%s on the keyed database: exit %d stdout %q stderr %q", strings.Join(args, " "), ran.code, ran.stdout, ran.stderr)
		}
	}
	if isPlaintextSQLite(t, path) {
		t.Fatal("the keyed one-shot commands left a plaintext database")
	}
	if _, err := db.Connect(path, db.WithKey(db.Key{})); !errors.Is(err, db.ErrKeyMissing) {
		t.Errorf("opening the keyed database without a key: %v, want %v", err, db.ErrKeyMissing)
	}
	other, err := db.ParseKey(otherKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Connect(path, db.WithKey(other)); !errors.Is(err, db.ErrWrongKey) {
		t.Errorf("opening the keyed database with another key: %v, want %v", err, db.ErrWrongKey)
	}

	plain := filepath.Join(dir, "plain.db")
	p, err := db.Connect(plain, db.WithKey(db.Key{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write.Exec("CREATE TABLE marker (x)"); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_DB", plain)
	for _, args := range keyedOneShots {
		ran := runApp(t, args...)
		if ran.code == cli.ExitOK || !strings.Contains(ran.stderr, string(db.ErrPlaintextDB)) {
			t.Errorf("%s with a key on a plaintext database: exit %d stderr %q, want the plaintext refusal", strings.Join(args, " "), ran.code, ran.stderr)
		}
	}
	if !isPlaintextSQLite(t, plain) {
		t.Error("a refused command rewrote the plaintext database")
	}
}
