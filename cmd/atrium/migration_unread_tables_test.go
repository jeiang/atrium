package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

var unreadTables = []string{
	"nightly_observations", "agent_profile", "notion_pages", "notion_sources", "notion_state", "notion_fts", "wiki_pages",
	"embed_notion_0", "embed_notion_3", "action_links", "rebalance_candidates", "pass_runs", "pass_run_alerts", "snoozes",
}

func TestMigrationTwoDropsUnreadTablesAndKeepsCarriedRows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	copyFile(t, filepath.Join(golden.Dir(), "world.db"), path)

	vector := "[" + strings.TrimSuffix(strings.Repeat("0.5,", 768), ",") + "]"
	old := mustConnect(t, path, db.Key{})
	for _, stmt := range []string{
		"CREATE VIRTUAL TABLE embed_notion_3 USING vec0(visibility TEXT PARTITION KEY, embedding FLOAT[768] DISTANCE_METRIC=cosine)",
		"INSERT INTO embed_notion_3 (rowid, visibility, embedding) VALUES (1, 'personal', '" + vector + "')",
		"INSERT INTO nightly_observations (subject, value, first_seen, last_seen) VALUES ('s', 'v', 1, 1)",
		"INSERT INTO notion_fts (title, path, body) VALUES ('Old page', 'Root', 'old body')",
		"INSERT INTO action_links (nonce, proposal_id, content_hash, expires_ts, created_ts) VALUES ('n1', 1, 'h', 2, 1)",
		"INSERT INTO pass_runs (job, started_at, finished_at, outcome, message) VALUES ('backup', 1, 2, 'failed', 'legacy')",
		"INSERT INTO pass_run_alerts (job, notified_at) VALUES ('backup', 1)",
		"INSERT INTO snoozes (proposal_id, account, message_id, imap_name, uidvalidity, uid, due_ts, state, created_ts) VALUES (1, 'icloud', 1, 'INBOX', 1, 1, 2, 'returned', 1)",
		"INSERT INTO embed_notion_0 (rowid, visibility, embedding) VALUES (1, 'personal', '" + vector + "')",
		"INSERT INTO skills (name, description, body, updated_at) VALUES ('carried', 'carried skill', 'body', 1)",
		"INSERT INTO export_pages (target, record_id, remote_id, hash, updated_at) VALUES ('obsidian', 7, 'Notes/carried.md', 'h', 1)",
		"INSERT INTO attention_snoozes (kind, source_id, until, created_ts) VALUES ('task', '42', 4102444800, 1)",
	} {
		if _, err := old.Write.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	count := func(d *db.DB, query string) int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow(query).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	for _, table := range unreadTables {
		if count(old, "SELECT COUNT(*) FROM "+table) == 0 {
			t.Fatalf("the version 1 fixture holds no %s row", table)
		}
	}
	skills := count(old, "SELECT COUNT(*) FROM skills")
	records := count(old, "SELECT COUNT(*) FROM kb_records")
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	freshDir := t.TempDir()
	freshEnv := schemaBaselineEnv(freshDir, filepath.Join(freshDir, "fresh.db"), "")
	if got := runProcess(t, "settings list", freshEnv...); got.code != cli.ExitOK {
		t.Fatalf("creating a fresh database through CLI: %s", got.stderr)
	}
	migrateSchemaFixture(t, path, schemaBaselineDump(t, freshEnv))

	base, _, stop := startServeStoppable(t, dir)
	if status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, base+"/activity", nil); status != http.StatusOK {
		t.Fatalf("GET /activity after the migration = %d: %.300s", status, body)
	}
	stop()

	d := mustConnect(t, path, db.Key{})
	if n := count(d, "SELECT MAX(version) FROM schema_migrations"); n != 2 {
		t.Fatalf("schema version %d after start, want 2", n)
	}
	for _, table := range unreadTables {
		if n := count(d, "SELECT COUNT(*) FROM sqlite_master WHERE tbl_name = '"+table+"' OR name = '"+table+"' OR name LIKE '"+table+"\\_%' ESCAPE '\\'"); n != 0 {
			t.Errorf("%d schema objects of %s remain", n, table)
		}
	}
	if n := count(d, "SELECT COUNT(*) FROM sqlite_sequence WHERE name = 'pass_runs'"); n != 0 {
		t.Errorf("the pass_runs sequence row remains")
	}
	if n := count(d, "SELECT COUNT(*) FROM skills"); n != skills {
		t.Errorf("%d skills rows, want %d", n, skills)
	}
	if n := count(d, "SELECT COUNT(*) FROM skills WHERE name = 'carried'"); n != 1 {
		t.Errorf("the carried skill is gone")
	}
	if n := count(d, "SELECT COUNT(*) FROM export_pages WHERE target = 'obsidian' AND record_id = 7 AND remote_id = 'Notes/carried.md'"); n != 1 {
		t.Errorf("the carried export page is gone")
	}
	if n := count(d, "SELECT COUNT(*) FROM attention_snoozes WHERE kind = 'task' AND source_id = '42'"); n != 1 {
		t.Errorf("the carried attention snooze is gone")
	}
	if n := count(d, "SELECT COUNT(*) FROM kb_records"); n != records {
		t.Errorf("%d knowledge records, want %d", n, records)
	}
	if n := count(d, "SELECT COUNT(*) FROM pragma_foreign_key_check"); n != 0 {
		t.Errorf("%d foreign key violations after the migration", n)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	check := runProcess(t, "db check", schemaBaselineEnv(dir, path, "")...)
	if check.code != cli.ExitOK || !strings.Contains(check.stdout, "integrity_check: ok\n") {
		t.Fatalf("db check after the migration: exit %d stdout %q stderr %q", check.code, check.stdout, check.stderr)
	}
	for _, table := range []string{"notion_fts", "embed_notion_0"} {
		if strings.Contains(check.stdout, table) {
			t.Errorf("db check still reports %s:\n%s", table, check.stdout)
		}
	}
}
