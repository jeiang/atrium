package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
)

const (
	rightKeyHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	otherKeyHex = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
)

type cliRun struct {
	code           int
	stdout, stderr string
}

func runProcess(t *testing.T, args string, env ...string) cliRun {
	t.Helper()
	cmd := cliExitProcess(t, args)
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return cliRun{exitCode(cmd.Run()), stdout.String(), stderr.String()}
}

func isPlaintextSQLite(t *testing.T, path string) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.HasPrefix(raw, []byte("SQLite format 3\x00"))
}

const (
	kbHit      = "Alpha page\n"
	kbFTSQuery = "SELECT title FROM kb_records WHERE id IN (SELECT rowid FROM kb_fts WHERE kb_fts MATCH 'alpha');\n"
)

func kbKNNQuery() string {
	return "SELECT title FROM kb_records WHERE id IN (SELECT rowid FROM embed_kb_0 WHERE embedding MATCH '[1" + strings.Repeat(",0", config.EmbedDim-1) + "]' AND k = 1);\n"
}

func seedKBRecord(t *testing.T, path string, key db.Key) {
	t.Helper()
	d, err := db.Connect(path, db.WithKey(key))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(t.Context(), d, path); err != nil {
		t.Fatal(err)
	}
	vector := make([]float64, config.EmbedDim)
	vector[0] = 1
	if _, err := d.Write.Exec("INSERT INTO kb_records (id, title, body, scope, writer, created_at, updated_at) VALUES (1, 'Alpha page', 'alpha beta gamma', 'personal', 'operator', 0, 0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO embed_kb_0 (rowid, embedding) VALUES (1, ?)", embed.Serialize(vector)); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseKeyCreatesEncryptedDatabaseAndGuardsEveryOpen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_DB_KEY", "")
	setEnv(t, "ATRIUM_CONFIG", "")
	setEnv(t, "CREDENTIALS_DIRECTORY", "")
	rightFile := writeKey(t, dir, "right.key", rightKeyHex)
	keyEnv := func(hex string) string { return "ATRIUM_DB_KEY=" + hex }
	encrypted := filepath.Join(dir, "encrypted.db")
	plain := filepath.Join(dir, "plain.db")
	dbEnv := func(path string) string { return "ATRIUM_DB=" + path }
	knnQuery := kbKNNQuery()

	created := runProcess(t, "settings set tz UTC", dbEnv(encrypted), keyEnv(rightKeyHex))
	if created.code != cli.ExitOK || isPlaintextSQLite(t, encrypted) {
		t.Fatalf("keyed command on a fresh database exited %d, stderr %q, plaintext header %v; want 0 and an encrypted file", created.code, created.stderr, isPlaintextSQLite(t, encrypted))
	}

	k, err := db.ParseKey(rightKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	seedKBRecord(t, encrypted, k)

	knn := runProcessStdin(t, knnQuery, "db shell", dbEnv(encrypted), keyEnv(rightKeyHex))
	if knn.code != cli.ExitOK || knn.stdout != kbHit {
		t.Errorf("KNN query on the keyed database exited %d with stdout %q, stderr %q; want 0 and %q", knn.code, knn.stdout, knn.stderr, kbHit)
	}
	fts := runProcessStdin(t, kbFTSQuery, "db shell", dbEnv(encrypted), keyEnv(rightKeyHex))
	if fts.code != cli.ExitOK || fts.stdout != kbHit {
		t.Errorf("FTS query on the keyed database exited %d with stdout %q, stderr %q; want 0 and %q", fts.code, fts.stdout, fts.stderr, kbHit)
	}

	credentials := filepath.Join(dir, "credentials")
	if err := os.Mkdir(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	writeKey(t, credentials, db.KeyCredential, rightKeyHex)
	if got := runProcessStdin(t, knnQuery, "db shell", dbEnv(encrypted), "CREDENTIALS_DIRECTORY="+credentials); got.code != cli.ExitOK || got.stdout != kbHit {
		t.Errorf("LoadCredential key exited %d with stdout %q, stderr %q; want 0 and %q", got.code, got.stdout, got.stderr, kbHit)
	}
	if got := runProcessStdin(t, knnQuery, "db shell", dbEnv(encrypted), "ATRIUM_DB_KEY_FILE="+rightFile); got.code != cli.ExitOK || got.stdout != kbHit {
		t.Errorf("ATRIUM_DB_KEY_FILE exited %d with stdout %q, stderr %q; want 0 and %q", got.code, got.stdout, got.stderr, kbHit)
	}
	configFile := filepath.Join(dir, "atrium.toml")
	if err := os.WriteFile(configFile, []byte("[db]\nkey_file = \""+rightFile+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runProcessStdin(t, knnQuery, "db shell", dbEnv(encrypted), "ATRIUM_CONFIG="+configFile); got.code != cli.ExitError ||
		!strings.Contains(got.stderr, "ATRIUM_CONFIG is no longer read") || strings.Contains(got.stderr, rightKeyHex) {
		t.Errorf("ATRIUM_CONFIG naming the key file exited %d with stdout %q, stderr %q; want the removal refusal and no key material", got.code, got.stdout, got.stderr)
	}

	for name, env := range map[string][]string{
		"a missing ATRIUM_DB_KEY_FILE": {"ATRIUM_DB_KEY_FILE=" + filepath.Join(dir, "missing.key")},
		"ATRIUM_DB_KEY and its _FILE":  {keyEnv(rightKeyHex), "ATRIUM_DB_KEY_FILE=" + rightFile},
		"a path in ATRIUM_DB_KEY":      {keyEnv(rightFile)},
		"a short key in ATRIUM_DB_KEY": {keyEnv(rightKeyHex[:63])},
	} {
		got := runProcessStdin(t, knnQuery, "db shell", append([]string{dbEnv(encrypted)}, env...)...)
		if got.code != cli.ExitError || got.stdout != "" || strings.Contains(got.stderr, rightKeyHex) || strings.Contains(got.stderr, rightKeyHex[:63]) {
			t.Errorf("%s: exited %d, stdout %q, stderr %q; want %d, empty stdout and no key material", name, got.code, got.stdout, got.stderr, cli.ExitError)
		}
	}

	for _, tc := range []struct {
		env  []string
		want string
	}{
		{[]string{"ATRIUM_DB_KEY_FILE=" + filepath.Join(dir, "missing.key")}, "ATRIUM_DB_KEY_FILE"},
		{[]string{keyEnv(rightKeyHex), "ATRIUM_DB_KEY_FILE=" + rightFile}, "ATRIUM_DB_KEY and ATRIUM_DB_KEY_FILE"},
		{[]string{"ATRIUM_DB_KEY_FILE=" + writeKey(t, dir, "empty.key", "")}, "ATRIUM_DB_KEY_FILE"},
	} {
		if got := runProcessStdin(t, knnQuery, "db shell", append([]string{dbEnv(encrypted)}, tc.env...)...); !strings.Contains(got.stderr, tc.want) {
			t.Errorf("stderr %q does not name %q", got.stderr, tc.want)
		}
	}

	fresh := filepath.Join(dir, "fresh.db")
	if got := runProcess(t, "settings set tz UTC", dbEnv(fresh), "ATRIUM_DB_KEY_FILE="+writeKey(t, dir, "blank.key", "")); got.code != cli.ExitError {
		t.Errorf("empty ATRIUM_DB_KEY_FILE on a fresh database exited %d, stderr %q; want %d", got.code, got.stderr, cli.ExitError)
	}
	if _, err := os.Stat(fresh); err == nil {
		t.Errorf("empty ATRIUM_DB_KEY_FILE created a database file")
	}

	if got := runProcess(t, "settings set tz UTC", dbEnv(plain)); got.code != cli.ExitOK || !isPlaintextSQLite(t, plain) {
		t.Fatalf("command without a key exited %d, stderr %q; want 0 and a plaintext database", got.code, got.stderr)
	}

	for _, tc := range []struct {
		name string
		path string
		env  []string
		want string
	}{
		{"wrong key", encrypted, []string{keyEnv(otherKeyHex)}, string(db.ErrWrongKey)},
		{"no key on an encrypted database", encrypted, nil, string(db.ErrKeyMissing)},
		{"key on a plaintext database", plain, []string{keyEnv(rightKeyHex)}, string(db.ErrPlaintextDB)},
	} {
		before, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		got := runProcessStdin(t, kbFTSQuery, "db shell", append([]string{dbEnv(tc.path)}, tc.env...)...)
		after, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if got.code != cli.ExitError || got.stdout != "" || !strings.Contains(got.stderr, tc.want) {
			t.Errorf("%s: exited %d, stdout %q, stderr %q; want %d, empty stdout and %q", tc.name, got.code, got.stdout, got.stderr, cli.ExitError, tc.want)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("%s: the refused open changed the database file", tc.name)
		}
	}
}
