//go:build !modernc

package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func backupEnv(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_DB_KEY", "")
	t.Setenv("ATRIUM_CONFIG", "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
}

func seedBackupDB(t *testing.T, path string, key db.Key) {
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
	for _, stmt := range []string{
		"INSERT INTO notion_pages (rid, id, title, path, url, markdown) VALUES (1, 'p1', 'Alpha page', 'Root', 'https://notion.example/p1', 'alpha beta gamma')",
		"INSERT INTO notion_fts (rowid, title, path, body) VALUES (1, 'Alpha page', 'Root', 'alpha beta gamma')",
	} {
		if _, err := d.Write.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Write.Exec("INSERT INTO notion_vec (rowid, visibility, embedding) VALUES (1, 'public', ?)", embed.Serialize(vector)); err != nil {
		t.Fatal(err)
	}
}

func writeStateFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
}

func archiveEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	out := map[string][]byte{}
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name], _ = io.ReadAll(tr)
	}
}

func dirListing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestBackupRestoreEncryptedDatabase(t *testing.T) {
	dir := t.TempDir()
	backupEnv(t, dir)
	live := filepath.Join(dir, "state", "atrium.db")
	key, err := db.ParseKey(rightKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	seedBackupDB(t, live, key)
	state := filepath.Join(dir, "state")
	const secretText = "quarterly numbers: confidential"
	writeStateFiles(t, state, map[string]string{
		"files/threads/1/report.txt":   secretText,
		"files/empty.bin":              "",
		"files/projects/2/notes/a.txt": strings.Repeat("x", 200000),
		"pause":                        "paused",
		"media-link.key":               "media-secret-value",
	})
	if err := os.MkdirAll(filepath.Join(dir, "cache", "media"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStateFiles(t, filepath.Join(dir, "cache"), map[string]string{"media/movie.mkv": "cached"})
	env := []string{"ATRIUM_DB=" + live, "ATRIUM_DB_KEY=" + rightKeyHex}
	archive := filepath.Join(dir, "out", "atrium.backup")
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		t.Fatal(err)
	}

	writer, err := db.Connect(live, db.WithKey(key))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = writer.Write.Exec("INSERT INTO notion_pages (rid, id, title, path, url, markdown) VALUES (?, ?, 't', 'Root', 'u', 'm')", 1000+i, fmt.Sprintf("c%d", i))
		}
	}()
	got := runProcess(t, "backup "+archive, env...)
	close(stop)
	wg.Wait()
	_ = writer.Close()
	if got.code != cli.ExitOK || !strings.Contains(got.stdout, "encrypted, key id "+dbkey.ID(key)) || !strings.Contains(got.stdout, "3 files") {
		t.Fatalf("backup exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	if left, _ := filepath.Glob(archive + ".*"); len(left) != 0 {
		t.Errorf("backup left %v", left)
	}
	if again := runProcess(t, "backup "+archive, env...); again.code != cli.ExitError || !strings.Contains(again.stderr, "already exists") {
		t.Errorf("backup over an existing archive exited %d, stderr %q", again.code, again.stderr)
	}

	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	for name, needle := range map[string][]byte{"hex key": []byte(rightKeyHex), "raw key": key.Raw(), "file text": []byte(secretText), "media key": []byte("media-secret-value")} {
		if bytes.Contains(raw, needle) {
			t.Errorf("the archive contains the %s", name)
		}
	}
	entries := archiveEntries(t, archive)
	names := map[string]bool{}
	for n := range entries {
		names[n] = true
	}
	for _, want := range []string{"header.json", "entries.json", "manifest.mac", "atrium.db", "files/threads/1/report.txt", "files/empty.bin", "files/projects/2/notes/a.txt"} {
		if !names[want] {
			t.Errorf("the archive lacks %s; has %v", want, names)
		}
		delete(names, want)
	}
	if len(names) != 0 {
		t.Errorf("the archive holds unexpected entries %v", names)
	}
	if bytes.HasPrefix(entries["atrium.db"], []byte("SQLite format 3\x00")) {
		t.Error("the archived database is plaintext")
	}
	header := string(entries["header.json"])
	if !strings.Contains(header, dbkey.ID(key)) || strings.Contains(header, rightKeyHex) {
		t.Errorf("header %q should record the key id and no key", header)
	}

	before := dirListing(t, state)
	verify := runProcess(t, "restore --verify "+archive, env...)
	for _, want := range []string{"integrity_check: ok\n", "fts5 notion_fts: 1\n", "vec0 notion_vec: 1\n", "knn notion_vec: ok\n", "files: 3\n", "nothing was written"} {
		if verify.code != cli.ExitOK || !strings.Contains(verify.stdout, want) {
			t.Errorf("restore --verify exited %d with stdout %q, stderr %q; want %q", verify.code, verify.stdout, verify.stderr, want)
		}
	}
	if after := dirListing(t, state); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("restore --verify changed the state directory: %v -> %v", before, after)
	}

	tampered := filepath.Join(dir, "tampered.backup")
	flipped := bytes.Clone(raw)
	flipped[len(flipped)/2] ^= 0xff
	if err := os.WriteFile(tampered, flipped, 0o600); err != nil {
		t.Fatal(err)
	}
	if bad := runProcess(t, "restore --verify "+tampered, env...); bad.code != cli.ExitError || !strings.Contains(bad.stderr, string(backup.ErrTampered)) {
		t.Errorf("verify of a tampered archive exited %d, stderr %q; want %d and %q", bad.code, bad.stderr, cli.ExitError, backup.ErrTampered)
	}
	if bad := runProcess(t, "restore --verify "+archive, "ATRIUM_DB="+live, "ATRIUM_DB_KEY="+otherKeyHex); bad.code != cli.ExitError || !strings.Contains(bad.stderr, string(backup.ErrArchiveKey)) || strings.Contains(bad.stderr+bad.stdout, otherKeyHex) {
		t.Errorf("verify with the wrong key exited %d, stderr %q", bad.code, bad.stderr)
	}
	if bad := runProcess(t, "restore --verify "+archive, "ATRIUM_DB="+live); bad.code != cli.ExitError || !strings.Contains(bad.stderr, "no key is configured") {
		t.Errorf("verify without a key exited %d, stderr %q", bad.code, bad.stderr)
	}
	if after := dirListing(t, state); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("a refused verify left files in the state directory: %v -> %v", before, after)
	}

	holder, err := db.Connect(live, db.WithKey(key))
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
	if busy := runProcess(t, "restore "+archive, env...); busy.code != cli.ExitError || !strings.Contains(busy.stderr, "restore refused") {
		t.Errorf("restore over a running instance exited %d, stderr %q", busy.code, busy.stderr)
	}
	_ = tx.Rollback()
	_ = holder.Close()

	if err := os.WriteFile(filepath.Join(state, "files", "stray.txt"), []byte("stray"), 0o600); err != nil {
		t.Fatal(err)
	}
	damaged, err := db.Connect(live, db.WithKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := damaged.Write.Exec("DELETE FROM notion_pages WHERE rid = 1"); err != nil {
		t.Fatal(err)
	}
	_ = damaged.Close()

	var collisions []string
	for i := -2; i < 120; i++ {
		c := live + ".pre-restore-" + time.Now().UTC().Add(time.Duration(i)*time.Second).Format("20060102T150405Z")
		collisions = append(collisions, c)
		if err := os.WriteFile(c, []byte("in the way"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if res := runProcess(t, "restore "+archive, env...); res.code != cli.ExitError || !strings.Contains(res.stderr, "already exists") {
		t.Fatalf("restore with a blocked swap exited %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}
	if body, err := os.ReadFile(filepath.Join(state, "files", "stray.txt")); err != nil || string(body) != "stray" {
		t.Errorf("a failed swap left files/stray.txt = %q, %v", body, err)
	}
	if left, _ := filepath.Glob(filepath.Join(state, "files.pre-restore-*")); len(left) != 0 {
		t.Errorf("a failed swap left %v", left)
	}
	intact, err := db.Connect(live, db.WithKey(key))
	if err != nil {
		t.Fatal(err)
	}
	var pages int
	if err := intact.Read.QueryRow("SELECT count(*) FROM notion_pages WHERE rid = 1").Scan(&pages); err != nil || pages != 0 {
		t.Errorf("a failed swap changed the live database: %d, %v", pages, err)
	}
	_ = intact.Close()
	for _, c := range collisions {
		_ = os.Remove(c)
	}

	res := runProcess(t, "restore "+archive, env...)
	if res.code != cli.ExitOK || !strings.Contains(res.stdout, "restored "+live) || strings.Count(res.stdout, "previous state kept at") != 2 {
		t.Fatalf("restore exited %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(state, "files", "stray.txt")); err == nil {
		t.Error("the restored files/ holds a file that is not in the archive")
	}
	keptFiles, _ := filepath.Glob(filepath.Join(state, "files.pre-restore-*"))
	if len(keptFiles) != 1 {
		t.Fatalf("restore kept %v, want the previous files/", keptFiles)
	}
	if body, err := os.ReadFile(filepath.Join(keptFiles[0], "stray.txt")); err != nil || string(body) != "stray" {
		t.Errorf("the kept files/ lost stray.txt: %q, %v", body, err)
	}
	keptDB, _ := filepath.Glob(live + ".pre-restore-*Z")
	if len(keptDB) != 1 {
		t.Fatalf("restore kept %v, want the previous database", keptDB)
	}
	previous, err := db.Connect(keptDB[0], db.WithKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if err := previous.Read.QueryRow("SELECT count(*) FROM notion_pages WHERE rid = 1").Scan(&pages); err != nil || pages != 0 {
		t.Errorf("the kept database does not hold the pre-restore state: %d, %v", pages, err)
	}
	_ = previous.Close()
	if body, err := os.ReadFile(filepath.Join(state, "files", "threads", "1", "report.txt")); err != nil || string(body) != secretText {
		t.Errorf("restored file = %q, %v", body, err)
	}
	for _, p := range []string{"pause", "media-link.key"} {
		if _, err := os.Stat(filepath.Join(state, p)); err != nil {
			t.Errorf("restore removed %s", p)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(state, ".atrium-restore-*")); len(left) != 0 {
		t.Errorf("restore left %v", left)
	}
	if knn := runProcess(t, "db check", env...); knn.code != cli.ExitOK || !strings.Contains(knn.stdout, "vec0 notion_vec: 1\n") {
		t.Errorf("db check after restore exited %d with stdout %q, stderr %q", knn.code, knn.stdout, knn.stderr)
	}
	restored, err := db.Connect(live, db.WithKey(key))
	if err != nil {
		t.Fatal(err)
	}
	var title string
	if err := restored.Read.QueryRow("SELECT title FROM notion_pages WHERE rid = 1").Scan(&title); err != nil || title != "Alpha page" {
		t.Errorf("restored page = %q, %v", title, err)
	}
	_ = restored.Close()

	other3 := filepath.Join(dir, "third", "atrium.db")
	third, err := db.ParseKey(thirdKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	seedBackupDB(t, other3, third)
	thirdEnv := []string{"ATRIUM_DB=" + other3, "ATRIUM_DB_KEY=" + rightKeyHex}
	if refused := runProcess(t, "restore "+archive, thirdEnv...); refused.code != cli.ExitError || !strings.Contains(refused.stderr, "cannot be opened and locked") {
		t.Errorf("restore over an unopenable database exited %d, stderr %q", refused.code, refused.stderr)
	}
	if forced := runProcess(t, "restore --replace-unopenable "+archive, thirdEnv...); forced.code != cli.ExitOK {
		t.Fatalf("forced restore exited %d, stderr %q", forced.code, forced.stderr)
	}
	if kept, _ := filepath.Glob(other3 + ".pre-restore-*Z"); len(kept) != 1 {
		t.Errorf("forced restore kept %v, want the old database", kept)
	}

	rekeyFile := writeKey(t, dir, "next.key", otherKeyHex)
	if got := runProcess(t, "db rekey --new-key-file "+rekeyFile, env...); got.code != cli.ExitOK {
		t.Fatalf("rekey exited %d: %s", got.code, got.stderr)
	}
	newEnv := []string{"ATRIUM_DB=" + live, "ATRIUM_DB_KEY=" + otherKeyHex}
	other, _ := db.ParseKey(otherKeyHex)
	mismatch := runProcess(t, "restore --verify "+archive, newEnv...)
	if mismatch.code != cli.ExitError || !strings.Contains(mismatch.stderr, "key id "+dbkey.ID(key)) {
		t.Errorf("verify of a pre-rekey archive under the new key exited %d, stderr %q; want the original key id named", mismatch.code, mismatch.stderr)
	}
	oldKey := writeKey(t, dir, "old.key", rightKeyHex)
	if ok := runProcess(t, "restore --verify --key-file "+oldKey+" "+archive, newEnv...); ok.code != cli.ExitOK || !strings.Contains(ok.stdout, "key id "+dbkey.ID(key)) {
		t.Errorf("verify of a pre-rekey archive with its key exited %d, stdout %q, stderr %q", ok.code, ok.stdout, ok.stderr)
	}
	postRekey := filepath.Join(dir, "post.backup")
	if got := runProcess(t, "backup "+postRekey, newEnv...); got.code != cli.ExitOK {
		t.Fatalf("backup after rekey exited %d: %s", got.code, got.stderr)
	}
	post := string(archiveEntries(t, postRekey)["header.json"])
	if !strings.Contains(post, dbkey.ID(other)) || !strings.Contains(post, dbkey.ID(key)) {
		t.Errorf("a backup after a rekey records %q; want both key ids", post)
	}
	if res := runProcess(t, "restore --key-file "+oldKey+" "+archive, newEnv...); res.code != cli.ExitOK || !strings.Contains(res.stdout, "key id "+dbkey.ID(key)) {
		t.Fatalf("restore of a pre-rekey archive exited %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}
	if got := runProcess(t, "db check", env...); got.code != cli.ExitOK || !strings.Contains(got.stdout, "key id "+dbkey.ID(key)) {
		t.Errorf("the restored database does not open under its original key: exit %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
}

func TestBackupRestorePlaintextMigratesOlderArchiveAndRefusesNewer(t *testing.T) {
	dir := t.TempDir()
	backupEnv(t, dir)
	live := filepath.Join(dir, "state", "atrium.db")
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	copyGoldenFile(t, filepath.Join(golden.Dir(), "atrium.db"), live)
	writeStateFiles(t, filepath.Dir(live), map[string]string{"files/threads/9/a.txt": "plain file"})
	env := []string{"ATRIUM_DB=" + live}
	archive := filepath.Join(dir, "plain.backup")
	if got := runProcess(t, "backup "+archive, env...); got.code != cli.ExitOK || !strings.Contains(got.stdout, "plaintext") {
		t.Fatalf("backup exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	if !bytes.Contains(archiveEntries(t, archive)["header.json"], []byte(`"schema":0`)) {
		t.Fatalf("the golden Phase A database should archive at schema 0: %s", archiveEntries(t, archive)["header.json"])
	}
	if keyed := runProcess(t, "restore --verify "+archive, "ATRIUM_DB="+live, "ATRIUM_DB_KEY="+rightKeyHex); keyed.code != cli.ExitError || !strings.Contains(keyed.stderr, string(backup.ErrPlaintextKeyed)) {
		t.Errorf("verify of a plaintext archive with a key configured exited %d, stderr %q", keyed.code, keyed.stderr)
	}
	fresh := filepath.Join(dir, "fresh", "atrium.db")
	freshEnv := []string{"ATRIUM_DB=" + fresh}
	verify := runProcess(t, "restore --verify "+archive, freshEnv...)
	if verify.code != cli.ExitOK || !strings.Contains(verify.stdout, "migrated to") || !strings.Contains(verify.stdout, "encryption: plaintext") {
		t.Fatalf("verify exited %d, stdout %q, stderr %q", verify.code, verify.stdout, verify.stderr)
	}
	if _, err := os.Stat(filepath.Dir(fresh)); err == nil {
		t.Error("verify created the state directory")
	}
	if res := runProcess(t, "restore "+archive, freshEnv...); res.code != cli.ExitOK {
		t.Fatalf("restore exited %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}
	if !isPlaintextSQLite(t, fresh) {
		t.Error("the restored database is not plaintext")
	}
	migrated, err := db.Connect(fresh)
	if err != nil {
		t.Fatal(err)
	}
	var newest int
	if err := migrated.Read.QueryRow("SELECT max(version) FROM schema_migrations").Scan(&newest); err != nil || newest != db.LatestSchemaVersion() {
		t.Errorf("restored schema version = %d, %v; want %d", newest, err, db.LatestSchemaVersion())
	}
	if _, err := migrated.Write.Exec("INSERT INTO schema_migrations (version, applied_at) VALUES (9999, 1.0)"); err != nil {
		t.Fatal(err)
	}
	_ = migrated.Close()
	newer := filepath.Join(dir, "newer.backup")
	if got := runProcess(t, "backup "+newer, "ATRIUM_DB="+fresh); got.code != cli.ExitOK {
		t.Fatalf("backup of the newer database exited %d: %s", got.code, got.stderr)
	}
	before, _ := os.ReadFile(live)
	if bad := runProcess(t, "restore "+newer, env...); bad.code != cli.ExitError || !strings.Contains(bad.stderr, string(backup.ErrNewerSchema)) {
		t.Errorf("restore of a newer-schema archive exited %d, stderr %q", bad.code, bad.stderr)
	}
	if after, _ := os.ReadFile(live); !bytes.Equal(before, after) {
		t.Error("the refused restore changed the database")
	}
}
