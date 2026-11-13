package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func TestMaintenanceAPI(t *testing.T) {
	f := newFixture(t, goldenData{}, nil, "")
	backups := t.TempDir()
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, f.dir, "host.toml", "[core]\nbackup_directory = \""+backups+"\"\nbackup_keep = 1\nbackup_keep_days = 0\n"))
	old := mustConnect(t, filepath.Join(f.dir, "atrium.db"), keyFor(t))
	if _, err := old.Write.Exec("INSERT INTO operation_audit (ts, operation, actor, source, outcome, target) VALUES (1, 'maintenance.backup', 'operator', 'cli', 'ok', ?)", filepath.Join(backups, "older.backup")); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	noPath := func(reply apiReply) {
		t.Helper()
		if strings.Contains(reply.body, backups) || strings.Contains(reply.body, f.dir) {
			t.Fatalf("a maintenance reply names a filesystem path: %s", reply.body)
		}
	}

	expectAPI(t, s.do(http.MethodPost, "/api/v1/maintenance/db-backups", token, map[string]any{"dest": filepath.Join(f.dir, "elsewhere.db")}), http.StatusBadRequest, "bad_request")
	made := s.do(http.MethodPost, "/api/v1/maintenance/db-backups", token, nil)
	noPath(made)
	name, _ := apiData(t, made, http.StatusCreated)["name"].(string)
	if name == "" || strings.ContainsAny(name, `/\`) {
		t.Fatalf("db backup name: %s", made.body)
	}
	if _, err := os.Stat(filepath.Join(backups, name)); err != nil {
		t.Fatalf("the backup is not in the backup directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "elsewhere.db")); err == nil {
		t.Fatal("a caller-chosen destination was written")
	}
	second := s.do(http.MethodPost, "/api/v1/maintenance/db-backups", token, nil)
	noPath(second)
	newer, _ := apiData(t, second, http.StatusCreated)["name"].(string)
	if newer == name {
		t.Fatalf("two database backups in one second share the name %s", name)
	}
	if found, _ := filepath.Glob(filepath.Join(backups, "atrium-*.db")); len(found) != 1 || filepath.Base(found[0]) != newer {
		t.Fatalf("database backups beyond backup_keep remain: %v", found)
	}
	name = newer

	archived := s.do(http.MethodPost, "/api/v1/maintenance/backups", token, nil)
	noPath(archived)
	first, _ := apiData(t, archived, http.StatusCreated)["name"].(string)
	archived = s.do(http.MethodPost, "/api/v1/maintenance/backups", token, nil)
	noPath(archived)
	archive, _ := apiData(t, archived, http.StatusCreated)["name"].(string)
	if found, _ := filepath.Glob(filepath.Join(backups, "atrium-*.backup")); archive == first || len(found) != 1 || filepath.Base(found[0]) != archive {
		t.Fatalf("archives beyond backup_keep remain: %s %s %v", first, archive, found)
	}
	verified := s.do(http.MethodPost, "/api/v1/maintenance/backups/verify", token, map[string]any{"name": archive})
	noPath(verified)
	apiData(t, verified, http.StatusOK)
	if err := os.WriteFile(filepath.Join(backups, "junk.backup"), []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	junk := s.do(http.MethodPost, "/api/v1/maintenance/backups/verify", token, map[string]any{"name": "junk.backup"})
	noPath(junk)
	if junk.status < 400 {
		t.Fatalf("a junk archive verified: %s", junk.body)
	}
	_ = os.Remove(filepath.Join(backups, "junk.backup"))
	for _, bad := range []string{"../atrium.db", "a/b", filepath.Join(f.dir, "atrium.db"), ""} {
		expectField(t, s.do(http.MethodPost, "/api/v1/maintenance/backups/verify", token, map[string]any{"name": bad}), "name")
	}

	activity := s.do(http.MethodGet, "/api/v1/activity?limit=100", token, nil)
	noPath(activity)
	for _, it := range apiItems(t, apiData(t, activity, http.StatusOK)) {
		for field, v := range it.(map[string]any) {
			if text, ok := v.(string); ok && (strings.HasPrefix(text, "/") || strings.Contains(text, ":/")) {
				t.Fatalf("activity field %s carries a path: %s", field, text)
			}
		}
	}
	for _, want := range []string{`"backup:` + name + `"`, `"backup:` + archive + `"`, `"older.backup"`} {
		if !strings.Contains(activity.body, want) {
			t.Fatalf("activity lacks %s: %.3000s", want, activity.body)
		}
	}

	checked := s.do(http.MethodPost, "/api/v1/maintenance/check", token, nil)
	noPath(checked)
	apiData(t, checked, http.StatusOK)
	if got := s.do(http.MethodGet, "/api/v1/maintenance/check", token, nil); got.status != http.StatusNotFound && got.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET /maintenance/check still runs: %d", got.status)
	}

	held, err := db.TryResourceLocks(filepath.Join(f.dir, "atrium.db"), []string{"backup"})
	if err != nil {
		t.Fatal(err)
	}
	busy := s.do(http.MethodPost, "/api/v1/maintenance/db-backups", token, nil)
	_ = held.Close()
	noPath(busy)
	expectAPI(t, busy, http.StatusConflict, "conflict")
	if entries, _ := os.ReadDir(backups); len(entries) != 2 {
		t.Fatalf("a refused backup wrote %d files", len(entries))
	}
}
