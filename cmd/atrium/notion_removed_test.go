package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestStoredNotionDataSurvivesTheRemoval(t *testing.T) {
	t.Parallel()
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	t.Cleanup(func() { _ = d.Close() })
	for _, stmt := range []string{
		"INSERT INTO connections (id, kind, label, config_source, config_json, enabled, state, created_at, updated_at) VALUES ('service/notion', 'service/notion', 'Notion', 'operator', '{\"api\":\"http://127.0.0.1:9\"}', 1, 'ok', 1, 1)",
		"INSERT INTO settings (key, value) VALUES ('wiki_root', '0123456789abcdef0123456789abcdef'), ('export_notion_enabled', 'true'), ('export_notion_parent', '0123456789abcdef0123456789abcdef'), ('notion_enabled', 'true')",
		"INSERT INTO jobs (name, prompt, schedule, tz, kind, state, next_due, created_by, created_at, module, declaration_key, activated_at) VALUES ('notion-sync', '', 'every 15m', 'UTC', 'builtin', 'active', 1, 'system', 1, 'notion', 'notion/notion-sync', 1)",
		"INSERT INTO kb_records (title, body, scope, writer, origin, created_at, updated_at) VALUES ('Lisbon apartment lease', 'The lease renews every March.', 'personal', 'import', 'notion:0123456789abcdef0123456789abcdef', 1, 1)",
		"INSERT INTO notion_pages (rid, id, title, path, url, markdown) VALUES (1, 'p1', 'Old page', 'Root', 'https://notion.example/p1', 'old')",
		"INSERT INTO export_pages (target, record_id, remote_id, hash, updated_at) VALUES ('notion', 0, 'p1', 'h', 1)",
	} {
		if _, err := d.Write.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	var recordID int64
	if err := d.Read.QueryRow("SELECT id FROM kb_records WHERE origin LIKE 'notion:%'").Scan(&recordID); err != nil {
		t.Fatal(err)
	}

	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatalf("serve with stored Notion data: %v", err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}

	found := false
	list, _ := apiData(t, s.do(http.MethodGet, "/api/v1/connections", token, nil), http.StatusOK)["connections"].([]any)
	for _, item := range list {
		c := item.(map[string]any)
		if c["id"] != "service/notion" {
			continue
		}
		found = true
		if c["enabled"] != false || c["state"] != "disabled" || !strings.Contains(fmt.Sprint(c["reason"]), "no longer supported") {
			t.Errorf("the stored Notion connection is not disabled as unsupported: %v", c)
		}
	}
	if !found {
		t.Error("the stored Notion connection disappeared from the list")
	}

	hits := apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/knowledge?q=Lisbon&state=all", token, nil), http.StatusOK))
	if len(hits) != 1 || hits[0].(map[string]any)["title"] != "Lisbon apartment lease" {
		t.Fatalf("knowledge search for the imported record: %v", hits)
	}
	record := apiData(t, s.do(http.MethodGet, fmt.Sprintf("/api/v1/knowledge/%d", recordID), token, nil), http.StatusOK)["record"].(map[string]any)
	if source, _ := record["source"].(map[string]any); source["origin"] != "notion:0123456789abcdef0123456789abcdef" {
		t.Errorf("the imported record lost its origin: %v", record)
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/wiki"},
		{http.MethodGet, "/api/v1/wiki/pages"},
		{http.MethodPost, "/api/v1/wiki/sync"},
		{http.MethodPost, "/api/v1/knowledge/import"},
	} {
		if got := s.do(tc.method, tc.path, token, map[string]any{}); got.status != http.StatusNotFound && got.status != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 404 or 405", tc.method, tc.path, got.status)
		}
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{"/library/wiki", "/notion/0123456789abcdef0123456789abcdef", "/settings/notion"} {
		if status, body, _ := fetch(t, client, http.MethodGet, base+path, nil); status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404: %.300s", path, status, body)
		}
	}
	for _, path := range []string{"/settings/connections", "/settings/export", "/activity", fmt.Sprintf("/library/knowledge/%d", recordID)} {
		status, body, _ := fetch(t, client, http.MethodGet, base+path, nil)
		if status != http.StatusOK || strings.Contains(body, `href="/notion/`) {
			t.Errorf("GET %s = %d: %.300s", path, status, body)
		}
	}

	var state string
	if err := d.Read.QueryRow("SELECT state FROM jobs WHERE declaration_key = 'notion/notion-sync'").Scan(&state); err != nil || state != "cancelled" {
		t.Errorf("the Notion sync job is %q (%v), want cancelled", state, err)
	}

	out := t.TempDir()
	if r := runApp(t, "catalog", "openapi", "--output", out); r.code != cli.ExitOK {
		t.Fatalf("catalog openapi: exit %d %s", r.code, r.stderr)
	}
	raw, err := os.ReadFile(filepath.Join(out, "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for path := range spec.Paths {
		if strings.HasPrefix(path, "/wiki") || path == "/knowledge/import" || strings.Contains(path, "notion") {
			t.Errorf("the API still describes %s", path)
		}
	}
}
