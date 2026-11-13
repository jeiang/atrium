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
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, f.dir, "host.toml", "[core]\nbackup_directory = \""+backups+"\"\n"))
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

	archived := s.do(http.MethodPost, "/api/v1/maintenance/backups", token, nil)
	noPath(archived)
	archive, _ := apiData(t, archived, http.StatusCreated)["name"].(string)
	verified := s.do(http.MethodPost, "/api/v1/maintenance/backups/verify", token, map[string]any{"name": archive})
	noPath(verified)
	apiData(t, verified, http.StatusOK)
	for _, bad := range []string{"../atrium.db", "a/b", filepath.Join(f.dir, "atrium.db"), ""} {
		expectField(t, s.do(http.MethodPost, "/api/v1/maintenance/backups/verify", token, map[string]any{"name": bad}), "name")
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
	expectAPI(t, busy, http.StatusConflict, "conflict")
	if entries, _ := os.ReadDir(backups); len(entries) != 2 {
		t.Fatalf("a refused backup wrote %d files", len(entries))
	}
}
