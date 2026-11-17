package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	kbexport "github.com/jeiang/atrium/internal/kb/export"
)

func exportSeed(t *testing.T, d *db.DB, query string, args ...any) int64 {
	t.Helper()
	res, err := d.Write.Exec(query, args...)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func exportRecord(t *testing.T, d *db.DB, title, body, scope, writer string, marks int64, private int64, thread, seq, run int64) int64 {
	t.Helper()
	var threadArg, seqArg, runArg any
	if thread != 0 {
		threadArg, seqArg, runArg = thread, seq, run
	}
	return exportSeed(t, d, `INSERT INTO kb_records (title, body, scope, private, marks, writer, source_thread_id, source_seq, source_run_id, confirmed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 100, 200)`, title, body, scope, private, marks, writer, threadArg, seqArg, runArg, nil)
}

func exportJob(t *testing.T, d *db.DB) int64 {
	t.Helper()
	var id int64
	if err := d.Read.QueryRow("SELECT id FROM jobs WHERE name = ?", "knowledge-export").Scan(&id); err != nil {
		t.Fatalf("knowledge-export job: %v", err)
	}
	return id
}

func exportRuns(t *testing.T, d *db.DB, jobID int64) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM job_runs WHERE job_id = ? AND ended_at IS NOT NULL", jobID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func runExport(t *testing.T, f *fixture, d *db.DB) {
	t.Helper()
	var jobID int64
	declared := func() bool {
		return d.Read.QueryRow("SELECT id FROM jobs WHERE name = ?", "knowledge-export").Scan(&jobID) == nil
	}
	if !declared() {
		serveUntil(t, f, "the knowledge-export job", declared)
	}
	asstExec(t, d, "UPDATE jobs SET next_due = NULL WHERE id != ?", jobID)
	before := exportRuns(t, d, jobID)
	schedulerDue(t, d, "knowledge-export")
	schedulerRun(t, f, d, func() bool { return exportRuns(t, d, jobID) > before })
}

func exportForm(t *testing.T, body, action string) ([][2]string, map[string]bool) {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(body))
	var fields [][2]string
	checked := map[string]bool{}
	inForm := false
	for {
		switch z.Next() {
		case html.ErrorToken:
			t.Fatalf("form %s not found or not closed", action)
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			attrs := map[string]string{}
			for _, a := range token.Attr {
				attrs[a.Key] = a.Val
			}
			if token.Data == "form" && attrs["action"] == action {
				inForm = true
			}
			if !inForm || token.Data != "input" || attrs["name"] == "" {
				continue
			}
			_, disabled := attrs["disabled"]
			_, selected := attrs["checked"]
			if attrs["type"] == "checkbox" {
				checked[attrs["name"]+"="+attrs["value"]] = selected
			}
			if !disabled && (attrs["type"] != "checkbox" || selected) {
				fields = append(fields, [2]string{attrs["name"], attrs["value"]})
			}
		case html.EndTagToken:
			if inForm && z.Token().Data == "form" {
				return fields, checked
			}
		}
	}
}

func exportPost(t *testing.T, w *asstWeb, action string, fields [][2]string, hx bool) (int, string, http.Header) {
	t.Helper()
	var encoded []string
	for _, field := range fields {
		encoded = append(encoded, url.QueryEscape(field[0])+"="+url.QueryEscape(field[1]))
	}
	return w.send(http.MethodPost, action, strings.NewReader(strings.Join(encoded, "&")), "application/x-www-form-urlencoded", hx)
}

func TestObsidianFolderIsHostConfiguration(t *testing.T) {
	t.Parallel()
	dir, _, _ := obsidianFixture(t)
	w := asstBrowse(t, startServeIn(t, dir))
	const action = "/settings/sections/export_obsidian"
	status, body, _ := w.get("/settings/export", false)
	if status != http.StatusOK {
		t.Fatalf("GET integrations = %d", status)
	}
	if strings.Contains(body, `name="export_obsidian_dir"`) {
		t.Errorf("the folder is still an editable control")
	}
	if !strings.Contains(body, "Not configured") {
		t.Errorf("the export settings do not say the folder is not configured")
	}
	fields, _ := exportForm(t, body, action)
	submitted := append(fields, [2]string{"export_obsidian_dir", "/srv/vault"})
	if status, body, _ := exportPost(t, w, action, submitted, false); status != http.StatusOK {
		t.Fatalf("folder save = %d: %.300s", status, body)
	}
}

func lastExportRun(t *testing.T, d *db.DB) (string, string) {
	t.Helper()
	var status, text string
	if err := d.Read.QueryRow("SELECT status, COALESCE(error, '') FROM job_runs WHERE job_id = ? ORDER BY id DESC LIMIT 1", exportJob(t, d)).Scan(&status, &text); err != nil {
		t.Fatal(err)
	}
	return status, text
}

func TestExportWithoutAFolderFails(t *testing.T) {
	t.Parallel()
	_, f, d := obsidianFixture(t)
	setEnv(t, settingEnv("public_url"), "https://atrium.invalid")
	if ran := runApp(t, "settings", "set", kbexport.ObsidianKindsKey, "pii,messages,personal"); ran.code != cli.ExitOK {
		t.Fatalf("settings set %s: %d %s", kbexport.ObsidianKindsKey, ran.code, ran.stderr)
	}
	exportRecord(t, d, "Dentist", "body", "personal", "chat", 0, 0, 0, 0, 0)
	runExport(t, f, d)
	var writes, mapped int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE origin = 'export'").Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM export_pages").Scan(&mapped); err != nil {
		t.Fatal(err)
	}
	if writes != 0 || mapped != 0 {
		t.Errorf("the export ran without a folder: %d writes, %d mappings", writes, mapped)
	}
	if status, text := lastExportRun(t, d); status != "error" || !strings.Contains(text, "export."+kbexport.ObsidianDirKey+" is not set") {
		t.Errorf("export without a folder = %s %q, want a failed run naming the missing folder", status, text)
	}
	var outcome string
	if err := d.Read.QueryRow("SELECT outcome FROM operation_audit WHERE operation = 'knowledge.export' ORDER BY id DESC LIMIT 1").Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "failed" {
		t.Errorf("audit outcome = %s, want failed", outcome)
	}
}

func TestExportToAnUnwritableFolderFails(t *testing.T) {
	t.Parallel()
	_, f, d := obsidianFixture(t)
	blocker := filepath.Join(t.TempDir(), "taken")
	if err := os.WriteFile(blocker, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(blocker, "vault")
	obsidianEnable(t, vault)
	exportRecord(t, d, "Dentist", "body", "personal", "chat", 0, 0, 0, 0, 0)
	runExport(t, f, d)
	if status, text := lastExportRun(t, d); status != "error" || !strings.Contains(text, "knowledge export folder "+vault+" is not writable") {
		t.Errorf("export to an unwritable folder = %s %q, want a failed run naming the folder", status, text)
	}
	var mapped int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM export_pages").Scan(&mapped); err != nil || mapped != 0 {
		t.Errorf("mappings after a failed export = %d, %v", mapped, err)
	}
}

func TestExportIgnoresRemovedTargetRowsAndUpdatesInPlace(t *testing.T) {
	t.Parallel()
	_, f, d := obsidianFixture(t)
	vault := filepath.Join(t.TempDir(), "knowledge")
	obsidianEnable(t, vault)
	id := exportRecord(t, d, "Dentist", "first body", "personal", "chat", 0, 0, 0, 0, 0)
	exportSeed(t, d, "INSERT INTO export_pages (target, record_id, remote_id, hash, updated_at) VALUES ('notion', ?, 'page-1', 'h1', 1), ('notion', 0, 'page-0', 'h0', 1)", id)
	runExport(t, f, d)
	if status, text := lastExportRun(t, d); status != "ok" {
		t.Fatalf("first export = %s %q", status, text)
	}
	path := filepath.Join(vault, "personal", "dentist-K"+fmt.Sprint(id)+".md")
	if content, err := os.ReadFile(path); err != nil || !strings.Contains(string(content), "first body") {
		t.Fatalf("first export file = %q, %v", content, err)
	}
	if _, err := d.Write.Exec("UPDATE kb_records SET body = 'second body', updated_at = 300 WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	runExport(t, f, d)
	if status, text := lastExportRun(t, d); status != "ok" {
		t.Fatalf("second export = %s %q", status, text)
	}
	content, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(content), "second body") || strings.Contains(string(content), "first body") {
		t.Errorf("updated export file = %q, %v", content, err)
	}
	entries, err := os.ReadDir(filepath.Join(vault, "personal"))
	var names []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "standing-instructions-") {
			names = append(names, e.Name())
		}
	}
	if err != nil || len(names) != 1 {
		t.Errorf("personal folder after two runs = %v, %v; want one file beside the standing instructions", names, err)
	}
	var notion, folder int
	if err := d.Read.QueryRow("SELECT COUNT(*) FILTER (WHERE target = 'notion'), COUNT(*) FILTER (WHERE target = 'obsidian') FROM export_pages "+
		"WHERE record_id NOT IN (SELECT id FROM kb_records WHERE origin = 'atrium:instructions')").Scan(&notion, &folder); err != nil {
		t.Fatal(err)
	}
	if notion != 2 || folder != 2 {
		t.Errorf("export_pages rows = %d notion, %d folder; want 2 and 2", notion, folder)
	}
}

func TestLegacyExportEffectDoesNotBlockObsidian(t *testing.T) {
	t.Parallel()
	_, f, d := obsidianFixture(t)
	vault := t.TempDir()
	obsidianEnable(t, vault)
	id := exportRecord(t, d, "Dentist", "body", "personal", "chat", 0, 0, 0, 0, 0)
	now := float64(time.Now().Unix())
	seedEffect(t, d, seededEffect{
		Origin: "export", Operation: "kb_export_create", OperationKey: "export:notion:K" + fmt.Sprint(id) + ":create",
		Direction: "forward", Target: "export:notion:K" + fmt.Sprint(id),
		State: "unknown", CreatedAt: now - 3600, LeaseAt: now - 3600, Settled: true,
	})
	runExport(t, f, d)
	var status, text string
	if err := d.Read.QueryRow("SELECT status, COALESCE(error, '') FROM job_runs ORDER BY id DESC LIMIT 1").Scan(&status, &text); err != nil {
		t.Fatal(err)
	}
	if status == "error" {
		t.Fatalf("a legacy export effect failed the job: %s", text)
	}
	if _, err := os.Stat(filepath.Join(vault, "personal", "dentist-K"+fmt.Sprint(id)+".md")); err != nil {
		t.Fatalf("a legacy export effect blocked the Obsidian export: %v", err)
	}
}

func obsidianFixture(t *testing.T) (string, *fixture, *db.DB) {
	t.Helper()
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "settings", "list"); ran.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	return dir, f, d
}

func obsidianEnable(t *testing.T, vault string) {
	t.Helper()
	setEnv(t, settingEnv("public_url"), "https://atrium.invalid")
	setEnv(t, settingEnv(kbexport.ObsidianDirKey), vault)
	for key, value := range map[string]string{
		kbexport.ObsidianKindsKey: "messages,personal",
	} {
		if ran := runApp(t, "settings", "set", key, value); ran.code != cli.ExitOK {
			t.Fatalf("settings set %s: %d %s", key, ran.code, ran.stderr)
		}
	}
}

func TestKnowledgeExportsToAnObsidianFolder(t *testing.T) {
	t.Parallel()
	dir, f, d := obsidianFixture(t)
	vault := t.TempDir()
	obsidianEnable(t, vault)
	loc, err := time.LoadLocation("America/Port_of_Spain")
	if err != nil {
		t.Fatal(err)
	}
	updated := time.Unix(200, 0).In(loc).Format("2006-01-02")
	threadID := exportSeed(t, d, "INSERT INTO threads (kind, title, effort, created_at, updated_at) VALUES ('chat', 'Trip planning', 'medium', 1, 1)")
	clean := exportRecord(t, d, "Dentist visit", "Dr Ng at the Main St clinic.", "personal", "chat", 0, 0, threadID, 3, 7)
	if _, err := d.Write.Exec("UPDATE kb_records SET confirmed_at = 150 WHERE id = ?", clean); err != nil {
		t.Fatal(err)
	}
	untrusted := exportRecord(t, d, "Flight", "See [the fare](https://example.com/fare) and ![map](https://example.com/map.png)", "personal", "chat", 1, 0, 0, 0, 0)
	pii := exportRecord(t, d, "Phone", "Call 555-0100", "personal", "chat", 2, 0, 0, 0, 0)
	private := exportRecord(t, d, "Diary", "private", "personal", "operator", 0, 1, 0, 0, 0)
	projectID := exportSeed(t, d, "INSERT INTO projects (name, effort, context_limit, created_at, updated_at, private) VALUES ('Secret', 'medium', 200000, 1, 1, 1)")
	secret := exportSeed(t, d, `INSERT INTO kb_records (title, body, scope, project_id, marks, writer, created_at, updated_at)
		VALUES ('Project secret', 'hidden', 'work', ?, 0, 'operator', 1, 1)`, projectID)
	stray := filepath.Join(vault, "mine.md")
	if err := os.WriteFile(stray, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	runExport(t, f, d)

	cleanPath := filepath.Join(vault, "personal", "dentist-visit-K"+fmt.Sprint(clean)+".md")
	content, err := os.ReadFile(cleanPath)
	if err != nil {
		t.Fatalf("clean record file: %v", err)
	}
	sourceLine := `source: "https://atrium.invalid/chat/` + fmt.Sprint(threadID) + `#m3"`
	for _, want := range []string{
		"atrium_id: K" + fmt.Sprint(clean),
		`title: "Dentist visit"`,
		"scope: personal",
		"writer: chat",
		"confirmed: true",
		"updated: " + updated,
		sourceLine,
		"Dr Ng at the Main St clinic.",
		"Source: [Trip planning](https://atrium.invalid/chat/" + fmt.Sprint(threadID) + "#m3)",
		"writer chat",
		"run 7",
	} {
		if !strings.Contains(string(content), want) {
			t.Errorf("clean file lacks %q: %s", want, content)
		}
	}
	untrustedPath := filepath.Join(vault, "personal", "flight-K"+fmt.Sprint(untrusted)+".md")
	untrustedContent, err := os.ReadFile(untrustedPath)
	if err != nil {
		t.Fatalf("untrusted record file: %v", err)
	}
	for _, want := range []string{`hxxps://example\[.\]com/fare`, `\[image: map\]`, "unconfirmed"} {
		if !strings.Contains(string(untrustedContent), want) {
			t.Errorf("untrusted file lacks %q: %s", want, untrustedContent)
		}
	}
	for _, banned := range []string{"](", "![", "https://example.com"} {
		if strings.Contains(string(untrustedContent), banned) {
			t.Errorf("untrusted file keeps %q: %s", banned, untrustedContent)
		}
	}
	for _, absent := range []string{
		filepath.Join(vault, "personal", "phone-K"+fmt.Sprint(pii)+".md"),
		filepath.Join(vault, "personal", "diary-K"+fmt.Sprint(private)+".md"),
		filepath.Join(vault, "work", "project-secret-K"+fmt.Sprint(secret)+".md"),
	} {
		if _, err := os.Stat(absent); !os.IsNotExist(err) {
			t.Errorf("%s exists, want it excluded", absent)
		}
	}
	index, err := os.ReadFile(filepath.Join(vault, "index.md"))
	if err != nil {
		t.Fatalf("index file: %v", err)
	}
	if !strings.Contains(string(index), "Dentist visit") || strings.Contains(string(index), "Phone") {
		t.Errorf("index = %s", index)
	}

	before, err := os.Stat(cleanPath)
	if err != nil {
		t.Fatal(err)
	}
	runExport(t, f, d)
	after, err := os.Stat(cleanPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("a second run rewrote an unchanged file")
	}

	if _, err := d.Write.Exec("UPDATE kb_records SET retracted_at = 300 WHERE id = ?", clean); err != nil {
		t.Fatal(err)
	}
	runExport(t, f, d)
	if _, err := os.Stat(cleanPath); !os.IsNotExist(err) {
		t.Errorf("retracted record file still exists")
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("a file atrium did not create was removed: %v", err)
	}
	var mapped int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM export_pages WHERE target = 'obsidian' AND record_id = ?", clean).Scan(&mapped); err != nil || mapped != 0 {
		t.Errorf("mapping row for the retracted record = %d, %v", mapped, err)
	}

	base, _, stop := startServeStoppable(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	if status, _, _ := fetch(t, client, http.MethodGet, base+"/settings/export", nil); status != http.StatusOK {
		t.Fatalf("GET integrations = %d", status)
	}
	form := url.Values{
		"export_obsidian_kinds":      {"messages", "personal"},
		"export_obsidian_private_to": {vault},
	}
	if status, body, _ := fetch(t, client, http.MethodPost, base+"/settings/sections/export_obsidian", form); status != http.StatusOK {
		t.Fatalf("POST export section = %d: %.300s", status, body)
	}
	status, _, _ := fetch(t, client, http.MethodGet, base+"/settings/export", nil)
	if status != http.StatusOK {
		t.Fatalf("GET integrations = %d", status)
	}
	var actor, source string
	if err := d.Read.QueryRow("SELECT actor, source FROM settings_audit WHERE key = ? ORDER BY id DESC LIMIT 1", kbexport.ObsidianPrivateKey).Scan(&actor, &source); err != nil {
		t.Fatalf("settings audit: %v", err)
	}
	if actor != "operator" || source != "ui" {
		t.Errorf("settings audit actor/source = %s/%s, want operator/ui", actor, source)
	}
	stop()
	runExport(t, f, d)
	if _, err := os.Stat(filepath.Join(vault, "personal", "diary-K"+fmt.Sprint(private)+".md")); err != nil {
		t.Errorf("private record missing after naming the folder: %v", err)
	}

	if os.Geteuid() != 0 {
		blocked := exportRecord(t, d, "Blocked", "new", "personal", "operator", 0, 0, 0, 0, 0)
		scopeDir := filepath.Join(vault, "personal")
		if err := os.Chmod(scopeDir, 0o555); err != nil {
			t.Fatal(err)
		}
		runExport(t, f, d)
		entries, err := os.ReadDir(scopeDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".tmp") {
				t.Errorf("a failed write left %s", entry.Name())
			}
		}
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM export_pages WHERE target = 'obsidian' AND record_id = ?", blocked).Scan(&mapped); err != nil || mapped != 0 {
			t.Errorf("a failed write recorded a mapping row: %d, %v", mapped, err)
		}
		blockedPath := filepath.Join(scopeDir, "blocked-K"+fmt.Sprint(blocked)+".md")
		if _, err := os.Stat(blockedPath); !os.IsNotExist(err) {
			t.Errorf("a failed write left a file")
		}
		if err := os.Chmod(scopeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		runExport(t, f, d)
		if _, err := os.Stat(blockedPath); err != nil {
			t.Errorf("the export did not resume after the write failed: %v", err)
		}
	}
}

func TestInterruptedObsidianWriteBlocksTheExport(t *testing.T) {
	t.Parallel()
	_, f, d := obsidianFixture(t)
	vault := t.TempDir()
	obsidianEnable(t, vault)
	id := exportRecord(t, d, "Dentist", "body", "personal", "chat", 0, 0, 0, 0, 0)
	now := float64(time.Now().Unix())
	res, err := d.Write.Exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, target, action_json, state) "+
		"VALUES (?, ?, 'stale', 'export', 'obsidian_write', 'stale', 'forward', ?, '{}', 'intent')",
		now-3600, now-3600, "obsidian:personal/dentist-K"+fmt.Sprint(id)+".md")
	if err != nil {
		t.Fatal(err)
	}
	effectID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	runExport(t, f, d)
	var status, text string
	if err := d.Read.QueryRow("SELECT status, COALESCE(error, '') FROM job_runs ORDER BY id DESC LIMIT 1").Scan(&status, &text); err != nil {
		t.Fatal(err)
	}
	if status != "error" || !strings.Contains(text, fmt.Sprint(effectID)) {
		t.Fatalf("job run = %s %q, want an error naming write %d", status, text, effectID)
	}
	path := filepath.Join(vault, "personal", "dentist-K"+fmt.Sprint(id)+".md")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a blocked export wrote the file: %v", err)
	}
	if ran := runApp(t, "effects", "resolve", fmt.Sprint(effectID), "--outcome", "not-applied"); ran.code != cli.ExitOK {
		t.Fatalf("effects resolve: %d %s", ran.code, ran.stderr)
	}
	runExport(t, f, d)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the export did not resume after the write was resolved: %v", err)
	}
}

func TestObsidianRenameKeepsTheMappingUntilTheOldFileIsRemoved(t *testing.T) {
	t.Parallel()
	_, f, d := obsidianFixture(t)
	vault := t.TempDir()
	obsidianEnable(t, vault)
	id := exportRecord(t, d, "Dentist", "body", "personal", "chat", 0, 0, 0, 0, 0)
	runExport(t, f, d)
	oldRel := "personal/dentist-K" + fmt.Sprint(id) + ".md"
	oldPath := filepath.Join(vault, oldRel)
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("first export: %v", err)
	}
	if _, err := d.Write.Exec("UPDATE kb_records SET title = 'Dentist appointment', scope = 'work', updated_at = 300 WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(oldPath), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(oldPath), 0o755) })
	runExport(t, f, d)
	newRel := "work/dentist-appointment-K" + fmt.Sprint(id) + ".md"
	if _, err := os.Stat(filepath.Join(vault, newRel)); err != nil {
		t.Fatalf("the renamed file: %v", err)
	}
	var remote string
	if err := d.Read.QueryRow("SELECT remote_id FROM export_pages WHERE target = 'obsidian' AND record_id = ?", id).Scan(&remote); err != nil {
		t.Fatal(err)
	}
	if remote != oldRel {
		t.Fatalf("the mapping moved to %q while the old file could not be removed", remote)
	}
	if err := os.Chmod(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runExport(t, f, d)
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("the old file survived the rename: %v", err)
	}
	if err := d.Read.QueryRow("SELECT remote_id FROM export_pages WHERE target = 'obsidian' AND record_id = ?", id).Scan(&remote); err != nil {
		t.Fatal(err)
	}
	if remote != newRel {
		t.Fatalf("mapping remote id = %q, want %q", remote, newRel)
	}
}

func TestUndoingAnObsidianExportRemovesItsMapping(t *testing.T) {
	t.Parallel()
	_, f, d := obsidianFixture(t)
	vault := t.TempDir()
	obsidianEnable(t, vault)
	id := exportRecord(t, d, "Dentist", "body", "personal", "chat", 0, 0, 0, 0, 0)
	runExport(t, f, d)
	rel := "personal/dentist-K" + fmt.Sprint(id) + ".md"
	path := filepath.Join(vault, rel)
	var effectID int64
	if err := d.Read.QueryRow("SELECT id FROM effects WHERE operation = 'obsidian_write' AND direction = 'forward' AND target = ? ORDER BY id DESC LIMIT 1",
		"obsidian:"+rel).Scan(&effectID); err != nil {
		t.Fatal(err)
	}
	if got := runApp(t, "effects", "undo", fmt.Sprint(effectID)); got.code != cli.ExitOK {
		t.Fatalf("effects undo: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the undo left the exported file: %v", err)
	}
	var mapped int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM export_pages WHERE target = 'obsidian' AND record_id = ?", id).Scan(&mapped); err != nil {
		t.Fatal(err)
	}
	if mapped != 0 {
		t.Fatalf("%d mapping rows survived the undo, want 0", mapped)
	}
	runExport(t, f, d)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the export did not re-create the undone file: %v", err)
	}
}
