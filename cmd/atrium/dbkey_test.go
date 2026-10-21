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

func writeKey(t *testing.T, dir, name, hex string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(hex+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func isPlaintextSQLite(t *testing.T, path string) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.HasPrefix(raw, []byte("SQLite format 3\x00"))
}

func TestDatabaseKeyCreatesEncryptedDatabaseAndGuardsEveryOpen(t *testing.T) {
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
	t.Setenv("ATRIUM_EMBED_URL", embedder.URL)
	rightFile := writeKey(t, dir, "right.key", rightKeyHex)
	keyEnv := func(hex string) string { return "ATRIUM_DB_KEY=" + hex }
	encrypted := filepath.Join(dir, "encrypted.db")
	plain := filepath.Join(dir, "plain.db")
	dbEnv := func(path string) string { return "ATRIUM_DB=" + path }

	created := runProcess(t, "notion search alpha", dbEnv(encrypted), keyEnv(rightKeyHex))
	if created.code != cli.ExitOK || isPlaintextSQLite(t, encrypted) {
		t.Fatalf("keyed command on a fresh database exited %d, stderr %q, plaintext header %v; want 0 and an encrypted file", created.code, created.stderr, isPlaintextSQLite(t, encrypted))
	}

	k, err := db.ParseKey(rightKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := db.Connect(encrypted, db.WithKey(k))
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
	const hit = "Alpha page\thttps://notion.example/p1\n"

	knn := runProcess(t, "notion search zzz", dbEnv(encrypted), keyEnv(rightKeyHex))
	if knn.code != cli.ExitOK || knn.stdout != hit {
		t.Errorf("KNN search on the keyed database exited %d with stdout %q, stderr %q; want 0 and %q", knn.code, knn.stdout, knn.stderr, hit)
	}
	fts := runProcess(t, "notion search alpha", dbEnv(encrypted), keyEnv(rightKeyHex), "ATRIUM_EMBED_URL=http://127.0.0.1:1")
	if fts.code != cli.ExitOK || fts.stdout != hit {
		t.Errorf("FTS search on the keyed database exited %d with stdout %q, stderr %q; want 0 and %q", fts.code, fts.stdout, fts.stderr, hit)
	}

	credentials := filepath.Join(dir, "credentials")
	if err := os.Mkdir(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	writeKey(t, credentials, db.KeyCredential, rightKeyHex)
	if got := runProcess(t, "notion search zzz", dbEnv(encrypted), "CREDENTIALS_DIRECTORY="+credentials); got.code != cli.ExitOK || got.stdout != hit {
		t.Errorf("LoadCredential key exited %d with stdout %q, stderr %q; want 0 and %q", got.code, got.stdout, got.stderr, hit)
	}
	if got := runProcess(t, "notion search zzz", dbEnv(encrypted), "ATRIUM_DB_KEY_FILE="+rightFile); got.code != cli.ExitOK || got.stdout != hit {
		t.Errorf("ATRIUM_DB_KEY_FILE exited %d with stdout %q, stderr %q; want 0 and %q", got.code, got.stdout, got.stderr, hit)
	}
	configFile := filepath.Join(dir, "atrium.toml")
	if err := os.WriteFile(configFile, []byte("[db]\nkey_file = \""+rightFile+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runProcess(t, "notion search zzz", dbEnv(encrypted), "ATRIUM_CONFIG="+configFile); got.code != cli.ExitOK || got.stdout != hit {
		t.Errorf("db.key_file from ATRIUM_CONFIG exited %d with stdout %q, stderr %q; want 0 and %q", got.code, got.stdout, got.stderr, hit)
	}

	for name, env := range map[string][]string{
		"a missing ATRIUM_DB_KEY_FILE":   {"ATRIUM_DB_KEY_FILE=" + filepath.Join(dir, "missing.key")},
		"ATRIUM_DB_KEY and its _FILE":    {keyEnv(rightKeyHex), "ATRIUM_DB_KEY_FILE=" + rightFile},
		"key_file_file in ATRIUM_CONFIG": {"ATRIUM_CONFIG=" + writeConfig(t, dir, "[db]\nkey_file_file = \""+rightFile+"\"\n")},
		"a path in ATRIUM_DB_KEY":        {keyEnv(rightFile)},
		"a short key in ATRIUM_DB_KEY":   {keyEnv(rightKeyHex[:63])},
		"a literal key in ATRIUM_CONFIG": {"ATRIUM_CONFIG=" + writeConfig(t, dir, "[db]\nkey = \""+rightKeyHex+"\"\n")},
	} {
		got := runProcess(t, "notion search zzz", append([]string{dbEnv(encrypted)}, env...)...)
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
		if got := runProcess(t, "notion search zzz", append([]string{dbEnv(encrypted)}, tc.env...)...); !strings.Contains(got.stderr, tc.want) {
			t.Errorf("stderr %q does not name %q", got.stderr, tc.want)
		}
	}

	fresh := filepath.Join(dir, "fresh.db")
	if got := runProcess(t, "notion search alpha", dbEnv(fresh), "ATRIUM_DB_KEY_FILE="+writeKey(t, dir, "blank.key", "")); got.code != cli.ExitError {
		t.Errorf("empty ATRIUM_DB_KEY_FILE on a fresh database exited %d, stderr %q; want %d", got.code, got.stderr, cli.ExitError)
	}
	if _, err := os.Stat(fresh); err == nil {
		t.Errorf("empty ATRIUM_DB_KEY_FILE created a database file")
	}

	if got := runProcess(t, "notion search alpha", dbEnv(plain)); got.code != cli.ExitOK || !isPlaintextSQLite(t, plain) {
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
		got := runProcess(t, "notion search alpha", append([]string{dbEnv(tc.path)}, tc.env...)...)
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
