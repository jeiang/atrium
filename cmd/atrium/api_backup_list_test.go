package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
)

func TestBackupListThroughCLIAndAPI(t *testing.T) {
	f := newFixture(t, goldenData{}, nil, "")
	backups := filepath.Join(t.TempDir(), "backups")
	elsewhere := t.TempDir()
	host := writeFile(t, f.dir, "host.toml", "[core]\n")
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
	configure := func(dir string) {
		t.Helper()
		writeFile(t, f.dir, "host.toml", "[core]\nbackup_directory = \""+dir+"\"\nbackup_keep = 1\nbackup_keep_days = 0\n")
	}
	listJSON := func() map[string]any {
		t.Helper()
		got := runApp(t, "backup", "list", "--json")
		if got.code != cli.ExitOK {
			t.Fatalf("backup list --json exited %d: %s", got.code, got.stderr)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(got.stdout), &out); err != nil {
			t.Fatalf("backup list --json: %v: %s", err, got.stdout)
		}
		return out
	}
	state := func(list map[string]any) string {
		loc, _ := list["location"].(map[string]any)
		if loc["host_key"] != "core.backup_directory" || loc["nix_option"] != "services.atrium.backup.directory" {
			t.Fatalf("the location does not name where backups are configured: %v", loc)
		}
		s, _ := loc["state"].(string)
		return s
	}
	names := func(list map[string]any) []string {
		var out []string
		for _, it := range list["items"].([]any) {
			out = append(out, it.(map[string]any)["name"].(string))
		}
		return out
	}

	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	s := &apiSite{t: t, base: base}
	unconfigured := apiData(t, s.do(http.MethodGet, "/api/v1/maintenance/backups", token, nil), http.StatusOK)
	if state(unconfigured) != "unconfigured" || len(apiItems(t, unconfigured)) != 0 || unconfigured["total"] != float64(0) {
		t.Fatalf("no backup directory is not the typed empty state: %v", unconfigured)
	}
	stop()
	if list := listJSON(); state(list) != "unconfigured" || len(names(list)) != 0 {
		t.Fatalf("CLI without a backup directory: %v", list)
	}
	if got := runApp(t, "backup", "list"); got.code != cli.ExitOK || !strings.Contains(got.stdout, "core.backup_directory") {
		t.Fatalf("backup list without a directory: %d %q %q", got.code, got.stdout, got.stderr)
	}

	configure(backups)
	if list := listJSON(); state(list) != "missing" {
		t.Fatalf("a configured directory that does not exist: %v", list)
	}
	if err := os.Mkdir(backups, 0o750); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(backups, "atrium-20260101T000000Z-0000aaaa.backup")
	newer := filepath.Join(backups, "atrium-20260102T000000Z-0000bbbb.backup")
	outside := filepath.Join(elsewhere, "atrium-20260103T000000Z-0000cccc.backup")
	named := filepath.Join(elsewhere, "list")
	kept := filepath.Join(backups, "pre-D7-20260101T000000Z.archive")
	for _, dest := range []string{newer, older, outside, named, kept} {
		if got := runApp(t, "backup", dest); got.code != cli.ExitOK || !strings.Contains(got.stdout, "backup written to "+dest) {
			t.Fatalf("backup %s exited %d: %q %q", dest, got.code, got.stdout, got.stderr)
		}
	}
	if got := runApp(t, "backup", "--json", filepath.Join(elsewhere, "other.backup")); got.code != cli.ExitUsage {
		t.Fatalf("--json with a destination exited %d: %q", got.code, got.stderr)
	}
	if _, err := os.Lstat("list"); err == nil {
		t.Fatal("atrium backup list wrote an archive named list")
	}
	now := time.Now()
	if err := os.Chtimes(older, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(kept, now.Add(time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, backups, "notes.txt", "not a backup")
	writeFile(t, backups, "atrium-20260106T000000Z-0000ffff.backup", "not an archive either")
	writeFile(t, backups, ".atrium-snapshot.1", "staging")
	if err := os.Mkdir(filepath.Join(backups, "atrium-20260104T000000Z-0000dddd.backup"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(backups, "atrium-20260105T000000Z-0000eeee.backup")); err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Base(kept), filepath.Base(newer), filepath.Base(older)}

	base, _, stop, err = startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s = &apiSite{t: t, base: base}
	reply := s.do(http.MethodGet, "/api/v1/maintenance/backups", token, nil)
	if strings.Contains(reply.body, backups) || strings.Contains(reply.body, elsewhere) || strings.Contains(reply.body, f.dir) {
		t.Fatalf("the backup list names a filesystem path: %s", reply.body)
	}
	listed := apiData(t, reply, http.StatusOK)
	if state(listed) != "ready" || listed["total"] != float64(3) || listed["unexamined"] != float64(0) || strings.Join(names(listed), " ") != strings.Join(want, " ") {
		t.Fatalf("listed %v, want %v newest first: %s", names(listed), want, reply.body)
	}
	for i, it := range apiItems(t, listed) {
		item := it.(map[string]any)
		info, err := os.Stat(filepath.Join(backups, want[i]))
		if err != nil {
			t.Fatal(err)
		}
		header, _ := item["header"].(map[string]any)
		enc, _ := header["encryption"].(map[string]any)
		if item["size_bytes"] != float64(info.Size()) || item["format"] != float64(2) || item["problem"] != nil || enc["encrypted"] != false || header["created_at"] == "" || item["prunable"] != (i > 0) {
			t.Fatalf("archive %s is described as %v", want[i], item)
		}
		if _, err := time.Parse(time.RFC3339, item["modified_at"].(string)); err != nil {
			t.Fatalf("modified_at %v: %v", item["modified_at"], err)
		}
	}
	capped := apiData(t, s.do(http.MethodGet, "/api/v1/maintenance/backups?limit=1", token, nil), http.StatusOK)
	if strings.Join(names(capped), " ") != want[0] || capped["total"] != float64(3) {
		t.Fatalf("limit=1 gave %v of %v", names(capped), capped["total"])
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/maintenance/backups?limit=0", token, nil), http.StatusUnprocessableEntity, "invalid")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/maintenance/backups", "", nil), http.StatusUnauthorized, "unauthenticated")

	for _, name := range names(listed) {
		verified := apiData(t, s.do(http.MethodPost, "/api/v1/maintenance/backups/verify", token, map[string]any{"name": name}), http.StatusOK)
		if verified["name"] != name {
			t.Fatalf("verify %s answered %v", name, verified)
		}
	}
	for _, outsider := range []string{"../" + filepath.Base(elsewhere) + "/list", named, filepath.Join("..", filepath.Base(elsewhere), filepath.Base(outside))} {
		expectField(t, s.do(http.MethodPost, "/api/v1/maintenance/backups/verify", token, map[string]any{"name": outsider}), "name")
	}

	if cliList := listJSON(); strings.Join(names(cliList), " ") != strings.Join(want, " ") || cliList["total"] != float64(3) {
		t.Fatalf("CLI list %v differs from the API list %v", names(cliList), want)
	}
	text := runApp(t, "backup", "list")
	if text.code != cli.ExitOK || !strings.HasPrefix(text.stdout, want[0]+"\t") || !strings.Contains(text.stdout, "\tnamed, never pruned\t") || !strings.Contains(text.stdout, "\n"+want[1]+"\t") || !strings.HasSuffix(text.stdout, "3 of 3 archives\n") {
		t.Fatalf("backup list: %d %q %q", text.code, text.stdout, text.stderr)
	}

	fresh, _ := apiData(t, s.do(http.MethodPost, "/api/v1/maintenance/backups", token, nil), http.StatusCreated)["name"].(string)
	pruned := apiData(t, s.do(http.MethodGet, "/api/v1/maintenance/backups", token, nil), http.StatusOK)
	if got := strings.Join(names(pruned), " "); got != filepath.Base(kept)+" "+fresh || pruned["total"] != float64(2) {
		t.Fatalf("after a prune with backup_keep = 1 the list is %q, want the named archive and %s", got, fresh)
	}
	for _, gone := range []string{older, newer} {
		if _, err := os.Lstat(gone); err == nil {
			t.Fatalf("the prune kept %s", gone)
		}
	}
}
