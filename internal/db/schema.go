package db

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

//go:embed schema
var schemaFS embed.FS

type column struct{ name, ddl string }

var (
	runColumns = []column{
		{"limits_json", "TEXT"}, {"owner", "TEXT"}, {"lease_until", "REAL"}, {"skills_json", "TEXT"},
		{"superseded", "INTEGER NOT NULL DEFAULT 0"}, {"stop_requested", "INTEGER NOT NULL DEFAULT 0"},
	}
	threadColumns = []column{
		{"pinned", "INTEGER NOT NULL DEFAULT 0"}, {"context_limit", "INTEGER"},
		{"branched_from_thread_id", "INTEGER"}, {"branched_from_seq", "INTEGER"},
	}
	threadMessageColumns  = []column{{"system_sig", "TEXT"}, {"superseded", "INTEGER NOT NULL DEFAULT 0"}}
	subagentThreadColumns = []column{{"role", "TEXT"}, {"parent_run_id", "INTEGER"}}
	usageColumns          = []column{
		{"clear_edits", "INTEGER NOT NULL DEFAULT 0"}, {"cleared_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"compaction", "INTEGER NOT NULL DEFAULT 0"},
	}
	ledgerColumns     = []column{{"from_seq", "INTEGER"}, {"to_seq", "INTEGER"}}
	threadFileColumns = []column{
		{"anthropic_file_id", "TEXT"}, {"text", "TEXT"}, {"tokens", "INTEGER NOT NULL DEFAULT 0"}, {"project_id", "INTEGER"},
	}
	fileThreadColumns = []column{{"code_sandbox", "INTEGER NOT NULL DEFAULT 0"}, {"container_id", "TEXT"}, {"deleted_at", "REAL"}}
)

func initSchema(ctx context.Context, q Querier) error {
	steps := []func() error{
		script(ctx, q, "core"),
		script(ctx, q, "proposals"),
		script(ctx, q, "rebalance"),
		script(ctx, q, "live"),
		script(ctx, q, "notion_sync"),
		script(ctx, q, "notion_wiki"),
		script(ctx, q, "agent"),
		func() error { return relaxWrites(ctx, q) },
		columns(ctx, q, "runs", runColumns),
		columns(ctx, q, "threads", threadColumns),
		columns(ctx, q, "thread_messages", threadMessageColumns),
		script(ctx, q, "calendar"),
		script(ctx, q, "event_candidates"),
		script(ctx, q, "drafts"),
		columns(ctx, q, "drafts", []column{{"sent_msgid", "TEXT"}}),
		script(ctx, q, "scheduler"),
		columns(ctx, q, "jobs", []column{{"prompt_by", "TEXT"}}),
		script(ctx, q, "skills"),
		script(ctx, q, "notify"),
		columns(ctx, q, "threads", subagentThreadColumns),
		exec(ctx, q, "CREATE INDEX IF NOT EXISTS threads_parent_run ON threads (parent_run_id)"),
		script(ctx, q, "subagents"),
		script(ctx, q, "context"),
		columns(ctx, q, "usage", usageColumns),
		columns(ctx, q, "context_ledger", ledgerColumns),
		columns(ctx, q, "messages", []column{{"in_reply_to", "TEXT"}, {"refs", "TEXT"}}),
		script(ctx, q, "openloops"),
		script(ctx, q, "watches"),
		func() error { return dropRenewals(ctx, q) },
		script(ctx, q, "money"),
		func() error { return moneyColumns(ctx, q) },
		script(ctx, q, "media_channels"),
		script(ctx, q, "journal_queue"),
		script(ctx, q, "progress"),
		script(ctx, q, "workspace"),
		script(ctx, q, "projects"),
		columns(ctx, q, "threads", []column{{"project_id", "INTEGER"}}),
		exec(ctx, q, "CREATE INDEX IF NOT EXISTS threads_project ON threads (project_id, archived, updated_at)"),
		func() error { return relaxOwner(ctx, q) },
		columns(ctx, q, "thread_files", threadFileColumns),
		columns(ctx, q, "threads", fileThreadColumns),
		columns(ctx, q, "thread_queue", []column{{"files_json", "TEXT"}}),
		exec(ctx, q, "CREATE INDEX IF NOT EXISTS thread_files_project ON thread_files (project_id, id)"),
		func() error { return refreshViews(ctx, q) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func script(ctx context.Context, q Querier, name string) func() error {
	return func() error {
		text, err := schemaFS.ReadFile("schema/" + name + ".sql")
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, string(text)); err != nil {
			return fmt.Errorf("db: schema %s: %w", name, err)
		}
		return nil
	}
}

func exec(ctx context.Context, q Querier, stmt string) func() error {
	return func() error {
		if _, err := q.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("db: %s: %w", stmt, err)
		}
		return nil
	}
}

func columns(ctx context.Context, q Querier, table string, cols []column) func() error {
	return func() error {
		for _, c := range cols {
			if _, err := addColumn(ctx, q, table, c); err != nil {
				return err
			}
		}
		return nil
	}
}

type tableColumn struct {
	name    string
	notNull bool
}

func tableInfo(ctx context.Context, q Querier, table string) ([]tableColumn, error) {
	rows, err := q.QueryContext(ctx, "SELECT name, \"notnull\" FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []tableColumn
	for rows.Next() {
		var c tableColumn
		if err := rows.Scan(&c.name, &c.notNull); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func addColumn(ctx context.Context, q Querier, table string, c column) (bool, error) {
	cols, err := tableInfo(ctx, q, table)
	if err != nil {
		return false, err
	}
	for _, have := range cols {
		if strings.EqualFold(have.name, c.name) {
			return false, nil
		}
	}
	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, c.name, c.ddl)
	if _, err := q.ExecContext(ctx, stmt); err != nil {
		return false, fmt.Errorf("db: %s: %w", stmt, err)
	}
	return true, nil
}

func notNull(ctx context.Context, q Querier, table, column string) (bool, error) {
	cols, err := tableInfo(ctx, q, table)
	if err != nil {
		return false, err
	}
	for _, c := range cols {
		if c.name == column {
			return c.notNull, nil
		}
	}
	return false, fmt.Errorf("db: %s has no column %s", table, column)
}

func execAll(ctx context.Context, q Querier, stmts ...string) error {
	for _, s := range stmts {
		if _, err := q.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("db: %s: %w", s, err)
		}
	}
	return nil
}

func relaxWrites(ctx context.Context, q Querier) error {
	strict, err := notNull(ctx, q, "agent_writes", "thread_id")
	if err != nil || !strict {
		return err
	}
	return execAll(ctx, q,
		"CREATE TABLE agent_writes_new (id INTEGER PRIMARY KEY, ts REAL NOT NULL, "+
			"thread_id INTEGER REFERENCES threads(id), run_id INTEGER REFERENCES runs(id), tool TEXT NOT NULL, "+
			"target TEXT NOT NULL, before_json TEXT, after_json TEXT, undo_json TEXT, undone_at REAL)",
		"INSERT INTO agent_writes_new SELECT id, ts, thread_id, run_id, tool, target, before_json, after_json, undo_json, undone_at FROM agent_writes",
		"DROP TABLE agent_writes",
		"ALTER TABLE agent_writes_new RENAME TO agent_writes",
	)
}

func relaxOwner(ctx context.Context, q Querier) error {
	strict, err := notNull(ctx, q, "thread_files", "thread_id")
	if err != nil || !strict {
		return err
	}
	return execAll(ctx, q,
		"CREATE TABLE thread_files_new (id INTEGER PRIMARY KEY, thread_id INTEGER REFERENCES threads(id), run_id INTEGER, name TEXT NOT NULL, "+
			"media_type TEXT NOT NULL, bytes INTEGER NOT NULL DEFAULT 0, path TEXT NOT NULL, origin TEXT NOT NULL, created_at REAL NOT NULL)",
		"INSERT INTO thread_files_new SELECT id, thread_id, run_id, name, media_type, bytes, path, origin, created_at FROM thread_files",
		"DROP TABLE thread_files",
		"ALTER TABLE thread_files_new RENAME TO thread_files",
		"CREATE INDEX IF NOT EXISTS thread_files_thread ON thread_files (thread_id, id)",
	)
}

func dropRenewals(ctx context.Context, q Querier) error {
	var n int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'renewals'").Scan(&n); err != nil || n == 0 {
		return err
	}
	return execAll(ctx, q, "DROP TABLE renewals", "DELETE FROM watch_scans WHERE watch = 'renewals'")
}

func moneyColumns(ctx context.Context, q Querier) error {
	added, err := addColumn(ctx, q, "money_accounts", column{"forex", "INTEGER NOT NULL DEFAULT 0"})
	if err != nil {
		return err
	}
	if added {
		if _, err := q.ExecContext(ctx,
			"UPDATE money_accounts SET forex = 1 WHERE (bank = ? AND kind = ? AND currency = ?) OR (bank = ? AND currency = ?)",
			"republic", "credit_card", "TTD", "cibc", "BBD"); err != nil {
			return err
		}
	}
	_, err = addColumn(ctx, q, "transactions", column{"dismissed", "TEXT NOT NULL DEFAULT ''"})
	return err
}

func filedViewSQL(ctx context.Context, q Querier) (string, error) {
	cols, err := tableInfo(ctx, q, "messages")
	if err != nil {
		return "", err
	}
	parts := make([]string, len(cols))
	for i, c := range cols {
		switch c.name {
		case "folder":
			parts[i] = "COALESCE(c.folder, m.folder) AS folder"
		case "in_inbox":
			parts[i] = "CASE WHEN c.folder IS NULL THEN m.in_inbox ELSE 0 END AS in_inbox"
		default:
			parts[i] = "m." + c.name
		}
	}
	return "CREATE VIEW filed_messages AS SELECT " + strings.Join(parts, ", ") + " " +
		"FROM messages m LEFT JOIN corrections c ON c.message_id = m.id " +
		"WHERE m.gone_at IS NULL AND m.sent = 0 " +
		"AND COALESCE(c.folder, m.folder) IS NOT NULL AND COALESCE(c.folder, m.folder) != '' " +
		"AND (c.folder IS NOT NULL OR m.in_inbox = 0)", nil
}

func refreshViews(ctx context.Context, q Querier) error {
	wanted, err := filedViewSQL(ctx, q)
	if err != nil {
		return err
	}
	var current sql.NullString
	err = q.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type = 'view' AND name = 'filed_messages'").Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if current.Valid && current.String == wanted {
		return nil
	}
	return execAll(ctx, q, "DROP VIEW IF EXISTS filed_messages", wanted)
}

type seeds struct {
	Skills []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
	} `json:"skills"`
	Subagents []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
		Effort      string `json:"effort"`
		ToolsJSON   string `json:"tools_json"`
	} `json:"subagents"`
	Categories []string `json:"categories"`
}

func seed(ctx context.Context, q Querier, now time.Time) error {
	raw, err := schemaFS.ReadFile("schema/seeds.json")
	if err != nil {
		return err
	}
	var s seeds
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	ts := float64(now.UnixNano()) / 1e9
	for _, sk := range s.Skills {
		if _, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO skills (name, description, body, updated_at) VALUES (?, ?, ?, ?)",
			sk.Name, sk.Description, sk.Body, ts); err != nil {
			return err
		}
	}
	for _, a := range s.Subagents {
		if _, err := q.ExecContext(ctx,
			"INSERT OR IGNORE INTO subagents (name, description, prompt, effort, tools_json, enabled, builtin, updated_at) VALUES (?, ?, ?, ?, ?, 1, 1, ?)",
			a.Name, a.Description, a.Prompt, a.Effort, a.ToolsJSON, ts); err != nil {
			return err
		}
	}
	for _, c := range s.Categories {
		if _, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO categories (name) VALUES (?)", c); err != nil {
			return err
		}
	}
	return nil
}

var builtinSkills = sync.OnceValues(func() (map[string]bool, error) {
	raw, err := schemaFS.ReadFile("schema/seeds.json")
	if err != nil {
		return nil, err
	}
	var s seeds
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, sk := range s.Skills {
		names[sk.Name] = true
	}
	return names, nil
})

func BuiltinSkills() (map[string]bool, error) { return builtinSkills() }
