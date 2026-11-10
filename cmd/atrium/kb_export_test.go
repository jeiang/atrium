package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	kbexport "github.com/jeiang/atrium/internal/kb/export"
)

const exportParent = "0123456789abcdef0123456789abcdef"

type exportPage struct {
	parent   string
	title    string
	markdown string
	trashed  bool
}

type exportNotion struct {
	mu       sync.Mutex
	pages    map[string]*exportPage
	order    []string
	requests []string
	failNext bool
}

func (f *exportNotion) created() []*exportPage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*exportPage, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, f.pages[id])
	}
	return out
}

func (f *exportNotion) byTitle(title string) *exportPage {
	for _, page := range f.created() {
		if page.title == title {
			return page
		}
	}
	return nil
}

func (f *exportNotion) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func exportTitle(body struct {
	Title []struct {
		Text struct {
			Content string `json:"content"`
		} `json:"text"`
	} `json:"title"`
}) string {
	if len(body.Title) == 0 {
		return ""
	}
	return body.Title[0].Text.Content
}

func (f *exportNotion) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	reply := func(status int, payload any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	}
	missing := map[string]any{"object": "error", "status": 404, "code": "object_not_found", "message": "missing"}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/pages":
		var body struct {
			Parent struct {
				PageID string `json:"page_id"`
			} `json:"parent"`
			Properties struct {
				Title struct {
					Title []struct {
						Text struct {
							Content string `json:"content"`
						} `json:"text"`
					} `json:"title"`
				} `json:"title"`
			} `json:"properties"`
			Markdown string `json:"markdown"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.failNext {
			f.failNext = false
			if hijacker, ok := w.(http.Hijacker); ok {
				if conn, _, err := hijacker.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
		}
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", len(f.order)+1)
		f.pages[id] = &exportPage{parent: body.Parent.PageID, title: exportTitle(body.Properties.Title), markdown: body.Markdown}
		f.order = append(f.order, id)
		reply(http.StatusOK, map[string]any{"object": "page", "id": id, "url": "https://notion.invalid/" + id, "in_trash": false,
			"parent": map[string]any{"type": "page_id", "page_id": body.Parent.PageID}, "properties": map[string]any{}})
	case strings.HasPrefix(r.URL.Path, "/v1/pages/"):
		id, sub, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/pages/"), "/")
		page, ok := f.pages[id]
		if !ok {
			reply(http.StatusNotFound, missing)
			return
		}
		if sub == "markdown" && r.Method == http.MethodPatch {
			var body struct {
				Replace struct {
					NewStr string `json:"new_str"`
				} `json:"replace_content"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			page.markdown = body.Replace.NewStr
			reply(http.StatusOK, map[string]any{"object": "page_markdown", "id": id, "markdown": page.markdown, "truncated": false})
			return
		}
		if r.Method == http.MethodPatch {
			var body struct {
				InTrash    *bool `json:"in_trash"`
				Properties struct {
					Title struct {
						Title []struct {
							Text struct {
								Content string `json:"content"`
							} `json:"text"`
						} `json:"title"`
					} `json:"title"`
				} `json:"properties"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.InTrash != nil {
				page.trashed = *body.InTrash
			}
			if title := exportTitle(body.Properties.Title); title != "" {
				page.title = title
			}
		}
		reply(http.StatusOK, map[string]any{"object": "page", "id": id, "url": "https://notion.invalid/" + id, "in_trash": page.trashed,
			"parent": map[string]any{"type": "page_id", "page_id": page.parent}, "properties": map[string]any{}})
	default:
		reply(http.StatusNotFound, missing)
	}
}

func exportFixture(t *testing.T, fake *exportNotion) (string, *fixture, *db.DB) {
	t.Helper()
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	fake.pages = map[string]*exportPage{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	setEnv(t, "ATRIUM_NOTION_BASE_URL", srv.URL)
	if ran := runApp(t, "settings", "list"); ran.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	return dir, f, d
}

func exportEnable(t *testing.T, kinds string) {
	t.Helper()
	for key, value := range map[string]string{
		"public_url":              "https://atrium.invalid",
		kbexport.NotionEnabledKey: "true",
		kbexport.NotionParentKey:  exportParent,
		kbexport.NotionKindsKey:   kinds,
	} {
		if ran := runApp(t, "settings", "set", key, value); ran.code != cli.ExitOK {
			t.Fatalf("settings set %s: %d %s", key, ran.code, ran.stderr)
		}
	}
}

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
	start := func() (context.CancelFunc, chan appRun) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan appRun, 1)
		go func() { done <- runAppContext(t, f, ctx, "agent", "scheduler") }()
		return cancel, done
	}
	cancel, done := start()
	var jobID int64
	for range 100 {
		if err := d.Read.QueryRow("SELECT id FROM jobs WHERE name = ?", "knowledge-export").Scan(&jobID); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if jobID == 0 {
		t.Fatalf("the scheduler did not create the knowledge-export job")
	}
	if _, err := d.Write.Exec("UPDATE jobs SET next_due = NULL WHERE id != ?", jobID); err != nil {
		t.Fatal(err)
	}
	before := exportRuns(t, d, jobID)
	if _, err := d.Write.Exec("UPDATE jobs SET state = 'active', next_due = ?, lease_owner = NULL, lease_until = NULL WHERE id = ?",
		float64(time.Now().Unix())-1, jobID); err != nil {
		t.Fatal(err)
	}
	cancel, done = start()
	for range 200 {
		if exportRuns(t, d, jobID) > before {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if exportRuns(t, d, jobID) == before {
		t.Fatalf("the knowledge-export job did not run")
	}
}

func TestSchemaVersionSevenDatabaseMigratesToExportPages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	if code, _, stderr := runIn(t, dir, "settings", "list"); code != cli.ExitOK {
		t.Fatalf("settings list: exit %d %s", code, stderr)
	}
	d := mustConnect(t, path)
	for _, stmt := range []string{
		"DROP TABLE export_pages",
		"DELETE FROM schema_migrations WHERE version = 8",
	} {
		if _, err := d.Write.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.Close()
	if code, _, stderr := runIn(t, dir, "settings", "list"); code != cli.ExitOK {
		t.Fatalf("settings list on a version 7 database: exit %d %s", code, stderr)
	}
	check := mustConnect(t, path)
	var version int
	if err := check.Read.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil || version != db.LatestSchemaVersion() {
		t.Errorf("schema version = %d, %v; want %d", version, err, db.LatestSchemaVersion())
	}
	var pages int
	if err := check.Read.QueryRow("SELECT COUNT(*) FROM export_pages").Scan(&pages); err != nil || pages != 0 {
		t.Errorf("export_pages after migration = %d, %v; want an empty table", pages, err)
	}
	_ = check.Close()
	if code, stdout, stderr := runIn(t, dir, "db", "check"); code != cli.ExitOK || !strings.Contains(stdout, "integrity_check: ok") {
		t.Fatalf("db check: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	fresh := t.TempDir()
	if code, _, stderr := runIn(t, fresh, "settings", "list"); code != cli.ExitOK {
		t.Fatalf("settings list on a fresh database: exit %d %s", code, stderr)
	}
	migrated, created := schemaDump(t, path), schemaDump(t, filepath.Join(fresh, "atrium.db"))
	if !slices.Equal(migrated, created) {
		for _, line := range migrated {
			if !slices.Contains(created, line) {
				t.Errorf("only in the migrated schema: %s", line)
			}
		}
		for _, line := range created {
			if !slices.Contains(migrated, line) {
				t.Errorf("only in the fresh schema: %s", line)
			}
		}
	}
}

func TestKnowledgeExportsToNotionWithProvenance(t *testing.T) {
	fake := &exportNotion{}
	_, f, d := exportFixture(t, fake)
	exportEnable(t, "messages,personal")
	threadID := exportSeed(t, d, "INSERT INTO threads (kind, title, effort, created_at, updated_at) VALUES ('chat', 'Trip planning', 'medium', 1, 1)")
	clean := exportRecord(t, d, "Dentist", "Dr Ng at the Main St clinic.", "personal", "chat", 0, 0, threadID, 3, 7)
	if _, err := d.Write.Exec("UPDATE kb_records SET confirmed_at = 150 WHERE id = ?", clean); err != nil {
		t.Fatal(err)
	}
	exportRecord(t, d, "Flight", "See [the fare](https://example.com/fare) and ![map](https://example.com/map.png) and [[notes]]", "personal", "chat", 1, 0, 0, 0, 0)
	exportRecord(t, d, "Phone", "Call 555-0100", "personal", "chat", 2, 0, 0, 0, 0)
	exportRecord(t, d, "Diary", "private", "personal", "operator", 0, 1, 0, 0, 0)
	projectID := exportSeed(t, d, "INSERT INTO projects (name, effort, context_limit, created_at, updated_at, private) VALUES ('Secret', 'medium', 200000, 1, 1, 1)")
	exportSeed(t, d, `INSERT INTO kb_records (title, body, scope, project_id, marks, writer, created_at, updated_at)
		VALUES ('Project secret', 'hidden', 'work', ?, 0, 'operator', 1, 1)`, projectID)

	runExport(t, f, d)

	cleanPage := fake.byTitle("Dentist")
	if cleanPage == nil {
		t.Fatalf("no Dentist page: %+v", fake.created())
	}
	for _, want := range []string{"Dr Ng at the Main St clinic.", "Source: [Trip planning](https://atrium.invalid/chat/" + fmt.Sprint(threadID) + "#m3)", "writer chat", "run 7"} {
		if !strings.Contains(cleanPage.markdown, want) {
			t.Errorf("Dentist page lacks %q: %s", want, cleanPage.markdown)
		}
	}
	if strings.Contains(cleanPage.markdown, "unconfirmed") {
		t.Errorf("confirmed record is marked unconfirmed: %s", cleanPage.markdown)
	}
	untrusted := fake.byTitle("Flight")
	if untrusted == nil {
		t.Fatalf("no Flight page: %+v", fake.created())
	}
	for _, want := range []string{`hxxps://example\[.\]com/fare`, `\[image: map\]`, `\[\[notes\]\]`, "unconfirmed"} {
		if !strings.Contains(untrusted.markdown, want) {
			t.Errorf("untrusted page lacks %q: %s", want, untrusted.markdown)
		}
	}
	for _, banned := range []string{"](", "![", "https://example.com", "<a "} {
		if strings.Contains(untrusted.markdown, banned) {
			t.Errorf("untrusted page keeps %q: %s", banned, untrusted.markdown)
		}
	}
	if fake.byTitle("Phone") != nil {
		t.Errorf("pii record exported without its kind in the allowlist")
	}
	if fake.byTitle("Diary") != nil || fake.byTitle("Project secret") != nil {
		t.Errorf("private record exported without the destination switch")
	}
	index := fake.byTitle("Knowledge")
	if index == nil || !strings.Contains(index.markdown, "Dentist") || strings.Contains(index.markdown, "Phone") {
		t.Errorf("index page = %+v", index)
	}

	requests := fake.count()
	runExport(t, f, d)
	if fake.count() != requests {
		t.Errorf("a second run wrote %d Notion requests, want none", fake.count()-requests)
	}

	if _, err := d.Write.Exec("UPDATE kb_records SET retracted_at = 300 WHERE id = ?", clean); err != nil {
		t.Fatal(err)
	}
	runExport(t, f, d)
	if !cleanPage.trashed {
		t.Errorf("retracted record page is not in the trash")
	}
	var left int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM export_pages WHERE target = 'notion' AND record_id = ?", clean).Scan(&left); err != nil || left != 0 {
		t.Errorf("export_pages row for the retracted record = %d, %v", left, err)
	}
}

func TestPrivateExportNeedsTheDestinationNamed(t *testing.T) {
	fake := &exportNotion{}
	dir, f, d := exportFixture(t, fake)
	exportEnable(t, "pii,messages,personal")
	exportRecord(t, d, "Diary", "private", "personal", "operator", 0, 1, 0, 0, 0)
	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	if status, _, _ := fetch(t, client, http.MethodGet, base+"/settings/integrations", nil); status != http.StatusOK {
		t.Fatalf("GET integrations = %d", status)
	}
	save := func(private string) {
		t.Helper()
		form := url.Values{
			"export_notion_enabled":    {"true"},
			"export_notion_parent":     {exportParent},
			"export_notion_kinds":      {"pii", "messages", "personal"},
			"export_notion_private_to": {private},
		}
		if status, body, _ := fetch(t, client, http.MethodPost, base+"/settings/integrations/sections/export", form); status != http.StatusOK {
			t.Fatalf("POST export section = %d: %.300s", status, body)
		}
	}
	save("11111111111111111111111111111111")
	status, body, _ := fetch(t, client, http.MethodGet, base+"/settings/integrations", nil)
	if status != http.StatusOK {
		t.Fatalf("GET integrations = %d", status)
	}
	if !strings.Contains(body, `data-confirm-submit="Include private records in the export to `+exportParent+`?"`) {
		t.Errorf("the private switch does not name the destination")
	}
	runExport(t, f, d)
	if fake.byTitle("Diary") != nil {
		t.Errorf("private record exported to a destination that was not named")
	}
	save(exportParent)
	runExport(t, f, d)
	if fake.byTitle("Diary") == nil {
		t.Errorf("private record missing after naming the destination")
	}
	rows, err := d.Read.Query("SELECT actor, source, COALESCE(new_value, '') FROM settings_audit WHERE key = ? ORDER BY id", kbexport.NotionPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var audited []string
	for rows.Next() {
		var actor, source, value string
		if err := rows.Scan(&actor, &source, &value); err != nil {
			t.Fatal(err)
		}
		audited = append(audited, actor+"/"+source+"/"+value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(audited) != 2 || !strings.Contains(audited[0], "operator/ui/") || !strings.Contains(audited[1], exportParent) {
		t.Errorf("settings audit = %v", audited)
	}
}

func TestUnknownNotionWriteBlocksTheExport(t *testing.T) {
	fake := &exportNotion{}
	_, f, d := exportFixture(t, fake)
	exportEnable(t, "pii,messages,personal")
	exportRecord(t, d, "Dentist", "Dr Ng at the Main St clinic.", "personal", "chat", 0, 0, 0, 0, 0)
	fake.failNext = true
	runExport(t, f, d)
	var writeID int64
	if err := d.Read.QueryRow("SELECT id FROM agent_writes WHERE state = 'unknown' AND target LIKE 'export:notion:%'").Scan(&writeID); err != nil {
		t.Fatalf("unknown write after a dropped connection: %v", err)
	}
	var status, text string
	if err := d.Read.QueryRow("SELECT status, COALESCE(error, '') FROM job_runs ORDER BY id DESC LIMIT 1").Scan(&status, &text); err != nil {
		t.Fatal(err)
	}
	if status != "error" || !strings.Contains(text, fmt.Sprint(writeID)) {
		t.Errorf("job run = %s %q, want an error naming write %d", status, text, writeID)
	}
	requests := fake.count()
	runExport(t, f, d)
	if fake.count() != requests {
		t.Errorf("a blocked run made %d Notion requests", fake.count()-requests)
	}
	if ran := runApp(t, "writes", "resolve", fmt.Sprint(writeID)); ran.code != cli.ExitOK {
		t.Fatalf("writes resolve: %d %s", ran.code, ran.stderr)
	}
	runExport(t, f, d)
	if fake.byTitle("Dentist") == nil {
		t.Errorf("export did not resume after the write was resolved")
	}
}
