//go:build !modernc

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/embed"
)

const thirdKeyHex = "0123456789abcdeffedcba98765432100123456789abcdeffedcba9876543210"

func runProcessStdin(t *testing.T, stdin, args string, env ...string) cliRun {
	t.Helper()
	cmd := cliExitProcess(t, args)
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return cliRun{exitCode(cmd.Run()), stdout.String(), stderr.String()}
}

func TestDatabaseCommandsRekeyBackupCheckAndShellOnEncryptedDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_DB_KEY", "")
	t.Setenv("ATRIUM_CONFIG", "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	vector := make([]float64, config.EmbedDim)
	vector[0] = 1
	embedder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector}}})
	}))
	defer embedder.Close()
	t.Setenv("ATRIUM_MAIL_MAIL_EMBED_URL", embedder.URL)
	keyEnv := func(hex string) string { return "ATRIUM_DB_KEY=" + hex }
	dbEnv := func(path string) string { return "ATRIUM_DB=" + path }
	const hit = "Alpha page\thttps://notion.example/p1\n"
	live := filepath.Join(dir, "live.db")

	if got := runProcess(t, "notion search alpha", dbEnv(live), keyEnv(rightKeyHex)); got.code != cli.ExitOK {
		t.Fatalf("creating the encrypted database exited %d: %s", got.code, got.stderr)
	}
	k, err := db.ParseKey(rightKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := db.Connect(live, db.WithKey(k))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"INSERT INTO notion_pages (rid, id, title, path, url, markdown) VALUES (1, 'p1', 'Alpha page', 'Root', 'https://notion.example/p1', 'alpha beta gamma')",
		"INSERT INTO notion_fts (rowid, title, path, body) VALUES (1, 'Alpha page', 'Root', 'alpha beta gamma')",
	} {
		if _, err := seed.Write.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := seed.Write.Exec("INSERT INTO notion_vec (rowid, visibility, embedding) VALUES (1, 'public', ?)", embed.Serialize(vector)); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	check := runProcess(t, "db check", dbEnv(live), keyEnv(rightKeyHex))
	for _, want := range []string{"integrity_check: ok\n", "fts5 notion_fts: 1\n", "vec0 notion_vec: 1\n", "key id " + dbkey.ID(k)} {
		if check.code != cli.ExitOK || !strings.Contains(check.stdout, want) {
			t.Errorf("db check exited %d with stdout %q, stderr %q; want 0 and %q", check.code, check.stdout, check.stderr, want)
		}
	}

	plain := filepath.Join(dir, "plain.db")
	if got := runProcess(t, "notion search alpha", dbEnv(plain)); got.code != cli.ExitOK {
		t.Fatalf("creating the plaintext database exited %d: %s", got.code, got.stderr)
	}
	for name, tc := range map[string]struct {
		path string
		env  string
		want db.KeyError
	}{
		"wrong key":                 {live, keyEnv(otherKeyHex), db.ErrWrongKey},
		"no key on encrypted":       {live, keyEnv(""), db.ErrKeyMissing},
		"key on a plaintext db":     {plain, keyEnv(rightKeyHex), db.ErrPlaintextDB},
		"backup with the wrong key": {live, keyEnv(otherKeyHex), db.ErrWrongKey},
	} {
		args := "db check"
		if strings.HasPrefix(name, "backup") {
			args = "db backup " + filepath.Join(dir, "refused.db")
		}
		got := runProcess(t, args, dbEnv(tc.path), tc.env)
		if got.code != cli.ExitError || !strings.Contains(got.stderr, string(tc.want)) || strings.Contains(got.stderr+got.stdout, rightKeyHex) {
			t.Errorf("%s: exited %d, stdout %q, stderr %q; want %d and %q", name, got.code, got.stdout, got.stderr, cli.ExitError, tc.want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "refused.db")); err == nil {
		t.Error("a refused backup left a destination file")
	}
	if got := runProcess(t, "db check", dbEnv(filepath.Join(dir, "missing.db")), keyEnv(rightKeyHex)); got.code != cli.ExitError {
		t.Errorf("db check of a missing database exited %d, want %d", got.code, cli.ExitError)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing.db")); err == nil {
		t.Error("db check created the missing database")
	}

	backup := filepath.Join(dir, "backup.db")
	got := runProcess(t, "db backup "+backup, dbEnv(live), keyEnv(rightKeyHex))
	if got.code != cli.ExitOK || isPlaintextSQLite(t, backup) {
		t.Fatalf("db backup exited %d, stderr %q, plaintext %v; want 0 and an encrypted file", got.code, got.stderr, isPlaintextSQLite(t, backup))
	}
	again := runProcess(t, "db backup "+backup, dbEnv(live), keyEnv(rightKeyHex))
	if again.code != cli.ExitError || !strings.Contains(again.stderr, "already exists") {
		t.Errorf("backup over an existing file exited %d, stderr %q; want %d and a refusal", again.code, again.stderr, cli.ExitError)
	}
	if left, _ := filepath.Glob(backup + "*"); len(left) != 1 {
		t.Errorf("backup left %v, want only %s", left, backup)
	}
	fromBackup := runProcess(t, "db check", dbEnv(backup), keyEnv(rightKeyHex))
	if fromBackup.code != cli.ExitOK || fromBackup.stdout != strings.Replace(check.stdout, live, backup, 1) {
		t.Errorf("db check of the backup exited %d with stdout %q, stderr %q; want the live database's report %q", fromBackup.code, fromBackup.stdout, fromBackup.stderr, check.stdout)
	}
	if knn := runProcess(t, "notion search zzz", dbEnv(backup), keyEnv(rightKeyHex)); knn.code != cli.ExitOK || knn.stdout != hit {
		t.Errorf("KNN on the backup exited %d with stdout %q, stderr %q; want %q", knn.code, knn.stdout, knn.stderr, hit)
	}
	plainBackup := filepath.Join(dir, "plain-backup.db")
	if got := runProcess(t, "db backup "+plainBackup, dbEnv(plain)); got.code != cli.ExitOK || !isPlaintextSQLite(t, plainBackup) {
		t.Errorf("backup of the plaintext database exited %d, stderr %q, plaintext %v; want 0 and a plaintext copy", got.code, got.stderr, isPlaintextSQLite(t, plainBackup))
	}

	shell := runProcessStdin(t, "SELECT count(*) FROM notion_pages;\nSELECT title, 'a;b' FROM notion_pages WHERE rid = 1;\n.tables\n", "db shell", dbEnv(live), keyEnv(rightKeyHex))
	if shell.code != cli.ExitOK || !strings.HasPrefix(shell.stdout, "1\nAlpha page\ta;b\n") || !strings.Contains(shell.stdout, "notion_pages\n") {
		t.Errorf("db shell exited %d with stdout %q, stderr %q", shell.code, shell.stdout, shell.stderr)
	}
	trigger := runProcessStdin(t, "/* a; b */ CREATE TEMP TABLE t (a);\nCREATE TEMP TRIGGER tt AFTER INSERT ON t BEGIN\n  UPDATE t SET a = a + 1;\nEND;\nINSERT INTO t VALUES (7); SELECT a FROM t;\n", "db shell", dbEnv(live), keyEnv(rightKeyHex))
	if trigger.code != cli.ExitOK || trigger.stdout != "8\n" {
		t.Errorf("db shell with a block comment and a trigger exited %d with stdout %q, stderr %q; want 8", trigger.code, trigger.stdout, trigger.stderr)
	}
	if bad := runProcessStdin(t, "SELECT * FROM nope;\nSELECT 1;\n", "db shell", dbEnv(live), keyEnv(rightKeyHex)); bad.code != cli.ExitError || bad.stdout != "1\n" || !strings.Contains(bad.stderr, "nope") {
		t.Errorf("db shell with a failing statement exited %d, stdout %q, stderr %q; want %d, the later statement still run", bad.code, bad.stdout, bad.stderr, cli.ExitError)
	}
	if wrong := runProcessStdin(t, "SELECT 1;\n", "db shell", dbEnv(live), keyEnv(otherKeyHex)); wrong.code != cli.ExitError || wrong.stdout != "" {
		t.Errorf("db shell with the wrong key exited %d with stdout %q", wrong.code, wrong.stdout)
	}

	before, err := os.ReadFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	if got := runProcessStdin(t, otherKeyHex+"\n", "db rekey", dbEnv(plain)); got.code != cli.ExitError || !strings.Contains(got.stderr, "plaintext") {
		t.Errorf("rekey of a plaintext database exited %d, stderr %q; want %d and a refusal", got.code, got.stderr, cli.ExitError)
	}
	if after, _ := os.ReadFile(plain); !bytes.Equal(before, after) {
		t.Error("the refused rekey changed the plaintext database")
	}
	if got := runProcessStdin(t, otherKeyHex+"\n", "db rekey "+otherKeyHex, dbEnv(live), keyEnv(rightKeyHex)); got.code != cli.ExitUsage {
		t.Errorf("rekey with a key in argv exited %d, want %d", got.code, cli.ExitUsage)
	}
	for name, input := range map[string]string{"the current key": rightKeyHex + "\n", "a short key": otherKeyHex[:60] + "\n", "an empty input": ""} {
		before, _ := os.ReadFile(live)
		got := runProcessStdin(t, input, "db rekey", dbEnv(live), keyEnv(rightKeyHex))
		after, _ := os.ReadFile(live)
		if got.code != cli.ExitError || strings.Contains(got.stderr+got.stdout, otherKeyHex[:60]) || !bytes.Equal(before, after) {
			t.Errorf("rekey with %s exited %d, stdout %q, stderr %q, database changed %v; want %d and no change", name, got.code, got.stdout, got.stderr, !bytes.Equal(before, after), cli.ExitError)
		}
	}
	if left, _ := filepath.Glob(live + ".rekey*"); len(left) != 0 {
		t.Errorf("refused rekeys left %v", left)
	}

	other, err := db.ParseKey(otherKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	next := writeKey(t, dir, "next.key", otherKeyHex)
	holder, err := db.Connect(live, db.WithKey(k))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := holder.Write.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE notion_pages SET title = 'Alpha page' WHERE rid = 1"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		release func()
	}{{"an open write transaction", func() {}}, {"an idle connection", func() { _ = tx.Rollback() }}} {
		name, release := tc.name, tc.release
		busy := runProcess(t, "db rekey --new-key-file "+next, dbEnv(live), keyEnv(rightKeyHex))
		if busy.code != cli.ExitError || !strings.Contains(busy.stderr, "stop the atrium units") || strings.Contains(busy.stderr+busy.stdout, otherKeyHex) {
			t.Errorf("rekey with %s exited %d, stdout %q, stderr %q; want %d and a stop-the-units refusal", name, busy.code, busy.stdout, busy.stderr, cli.ExitError)
		}
		release()
	}
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(live + ".rekey*"); len(left) != 0 {
		t.Errorf("refused rekeys left %v", left)
	}
	if still := runProcess(t, "db check", dbEnv(live), keyEnv(rightKeyHex)); still.code != cli.ExitOK || still.stdout != check.stdout {
		t.Errorf("db check after refused rekeys exited %d with stdout %q, stderr %q; want the original report %q", still.code, still.stdout, still.stderr, check.stdout)
	}
	if moved := runProcess(t, "db check", dbEnv(live), keyEnv(otherKeyHex)); moved.code != cli.ExitError || !strings.Contains(moved.stderr, string(db.ErrWrongKey)) {
		t.Errorf("new key after refused rekeys exited %d, stderr %q; want %d and %q", moved.code, moved.stderr, cli.ExitError, db.ErrWrongKey)
	}

	rekey := runProcess(t, "db rekey --new-key-file "+next, dbEnv(live), keyEnv(rightKeyHex))
	wantLine := "key id " + dbkey.ID(k) + " -> " + dbkey.ID(other) + "\n"
	if rekey.code != cli.ExitOK || !strings.HasSuffix(rekey.stdout, wantLine) || strings.Contains(rekey.stdout+rekey.stderr, otherKeyHex) {
		t.Fatalf("db rekey exited %d with stdout %q, stderr %q; want 0 ending in %q", rekey.code, rekey.stdout, rekey.stderr, wantLine)
	}
	if isPlaintextSQLite(t, live) {
		t.Error("the rekeyed database has a plaintext header")
	}
	if left, _ := filepath.Glob(live + ".rekey*"); len(left) != 0 {
		t.Errorf("rekey left %v", left)
	}
	if old := runProcess(t, "db check", dbEnv(live), keyEnv(rightKeyHex)); old.code != cli.ExitError || !strings.Contains(old.stderr, string(db.ErrWrongKey)) {
		t.Errorf("old key after rekey exited %d, stderr %q; want %d and %q", old.code, old.stderr, cli.ExitError, db.ErrWrongKey)
	}
	reopened := runProcess(t, "db check", dbEnv(live), keyEnv(otherKeyHex))
	for _, want := range []string{"integrity_check: ok\n", "fts5 notion_fts: 1\n", "vec0 notion_vec: 1\n", "key id " + dbkey.ID(other)} {
		if reopened.code != cli.ExitOK || !strings.Contains(reopened.stdout, want) {
			t.Errorf("db check with the new key exited %d with stdout %q, stderr %q; want 0 and %q", reopened.code, reopened.stdout, reopened.stderr, want)
		}
	}
	if knn := runProcess(t, "notion search zzz", dbEnv(live), keyEnv(otherKeyHex)); knn.code != cli.ExitOK || knn.stdout != hit {
		t.Errorf("KNN with the new key exited %d with stdout %q, stderr %q; want %q", knn.code, knn.stdout, knn.stderr, hit)
	}

	stdinRekey := runProcessStdin(t, thirdKeyHex+"\n", "db rekey", dbEnv(live), keyEnv(otherKeyHex))
	if stdinRekey.code != cli.ExitOK {
		t.Fatalf("db rekey from standard input exited %d, stderr %q", stdinRekey.code, stdinRekey.stderr)
	}
	history := runProcessStdin(t, "SELECT key_id, replaced_by FROM db_key_history ORDER BY id;\n", "db shell", dbEnv(live), keyEnv(thirdKeyHex))
	third, err := db.ParseKey(thirdKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	wantHistory := dbkey.ID(k) + "\t" + dbkey.ID(other) + "\n" + dbkey.ID(other) + "\t" + dbkey.ID(third) + "\n"
	if history.code != cli.ExitOK || history.stdout != wantHistory {
		t.Errorf("recorded key history = %q (exit %d, stderr %q), want %q", history.stdout, history.code, history.stderr, wantHistory)
	}
}
