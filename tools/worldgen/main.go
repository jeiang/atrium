package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
)

type notionVector struct {
	RowID      int64  `json:"rowid"`
	Visibility string `json:"visibility"`
	Embedding  []byte `json:"embedding"`
}

var seededTables = map[string]bool{
	"schema_migrations": true, "db_key_history": true, "secrets": true,
	"settings_audit": true, "write_resolutions": true, "pass_runs": true, "pass_run_alerts": true,
	"endpoint_probes": true, "integration_health": true, "auth_sessions": true, "auth_logins": true,
	"auth_login_links": true, "mail_accounts": true, "ntfy_channels": true, "embed_index": true,
}

var ftsTables = map[string]bool{"message_fts": true, "thread_fts": true, "notion_fts": true}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: worldgen V1_SOURCE_DB OUTPUT_DIR")
		os.Exit(2)
	}
	if err := generate(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "worldgen:", err)
		os.Exit(1)
	}
}

func generate(source, out string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "atrium-worldgen-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	bin := filepath.Join(dir, "atrium")
	build := exec.Command("go", "build", "-o", bin, "./cmd/atrium")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build atrium: %w", err)
	}
	world := filepath.Join(dir, "atrium.db")
	accounts, err := accountsFile(dir)
	if err != nil {
		return err
	}
	accountsDef, ok := settings.LookupOf(config.KeyMailAccountsFile)
	if !ok {
		return fmt.Errorf("unregistered setting %s", config.KeyMailAccountsFile)
	}
	env := append(os.Environ(),
		"ATRIUM_DB="+world,
		"ATRIUM_CACHE_DIR="+filepath.Join(dir, "cache"),
		"ATRIUM_CONFIG=",
		"ATRIUM_DB_KEY=",
		config.EnvName(accountsDef)+"="+accounts,
	)
	for _, name := range module.Builtins {
		env = append(env, config.EnvName(module.FlagDef(name))+"=true")
	}
	atrium := func(stdin string, argv ...string) error {
		cmd := exec.Command(bin, argv...)
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		var stderr strings.Builder
		cmd.Stdout, cmd.Stderr = os.Stderr, &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("atrium %s: %w: %s", strings.Join(argv, " "), err, stderr.String())
		}
		return nil
	}
	if err := atrium("", "db", "import-v1", source); err != nil {
		return err
	}
	script, err := seedScript(world, source)
	if err != nil {
		return err
	}
	if err := atrium(script, "db", "shell"); err != nil {
		return err
	}
	if err := compact(world); err != nil {
		return err
	}
	if err := copyFile(world, filepath.Join(out, "world.db")); err != nil {
		return err
	}
	return writeVectors(source, filepath.Join(out, "notion_vec.json"))
}

func compact(world string) error {
	d, err := db.Connect(world)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	for _, table := range []string{"embed_message_0", "embed_notion_0"} {
		if _, err := d.Write.Exec("DELETE FROM " + table); err != nil {
			return err
		}
	}
	_, err = d.Write.Exec("VACUUM")
	return err
}

func accountsFile(dir string) (string, error) {
	secret := filepath.Join(dir, "icloud.password")
	path := filepath.Join(dir, "accounts.toml")
	body := fmt.Sprintf("[[account]]\nid = \"icloud\"\npreset = \"icloud\"\nlogin = \"operator@icloud.invalid\"\npassword_file = %q\n", secret)
	if err := os.WriteFile(secret, []byte("dummy-icloud-password\n"), 0o600); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(body), 0o600)
}

func copyFile(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o644)
}

func writeVectors(source, path string) error {
	v1, err := db.OpenSource(source, db.Key{})
	if err != nil {
		return err
	}
	defer func() { _ = v1.Close() }()
	rows, err := v1.Query("SELECT rowid, visibility, embedding FROM notion_vec")
	if err != nil {
		return err
	}
	vectors, err := db.Collect(rows, func(r *sql.Rows) (notionVector, error) {
		var v notionVector
		return v, r.Scan(&v.RowID, &v.Visibility, &v.Embedding)
	})
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func names(q db.Querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(row *sql.Rows) (string, error) {
		var name string
		return name, row.Scan(&name)
	})
}

func seedScript(target, source string) (string, error) {
	v2, err := db.Connect(target)
	if err != nil {
		return "", err
	}
	defer func() { _ = v2.Close() }()
	v1, err := db.OpenSource(source, db.Key{})
	if err != nil {
		return "", err
	}
	defer func() { _ = v1.Close() }()
	columns := func(q db.Querier, table string) ([]string, error) {
		return names(q, "SELECT name FROM pragma_table_info(?)", table)
	}
	tables, err := names(v2.Read, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return "", err
	}
	const unknownText = "cancelled, write outcome unknown"
	const partialText = "copied but not expunged: "
	const heldText = unknownText + ": write in flight"
	const settledText = "settled by operator after checking the mailbox"
	const stuckText = "interrupted before completion"
	var stmts []string
	for _, table := range tables {
		if seededTables[table] {
			continue
		}
		switch table {
		case "auto_actions":
			stmts = append(stmts, "INSERT OR IGNORE INTO main.auto_actions (id, ts, account, message_id, digest, kind, src, dest, before, after, undo, decision_id, state, error, undone_ts, undone_error, undo_started_at) "+
				"SELECT id, ts, account, message_id, digest, kind, src, dest, before, after, undo, decision_id, CASE "+
				"WHEN undone_ts IS NOT NULL THEN 'undone' WHEN undone_error = '"+heldText+"' THEN 'undo_held' "+
				"WHEN undone_error = '"+settledText+"' THEN 'undo_settled' WHEN instr(undone_error, '"+unknownText+"') > 0 THEN 'undo_unknown' "+
				"WHEN instr(undone_error, '"+partialText+"') > 0 THEN 'undo_partial' WHEN undone_error IS NOT NULL THEN 'undo_failed' "+
				"WHEN ok IS NULL AND error = '"+stuckText+"' THEN 'interrupted' WHEN ok IS NULL THEN 'held' WHEN ok = 1 THEN 'applied' "+
				"WHEN error LIKE 'skipped:%' THEN 'skipped' WHEN instr(error, '"+unknownText+"') > 0 THEN 'unknown' "+
				"WHEN instr(error, '"+partialText+"') > 0 THEN 'partial' ELSE 'failed' END, "+
				"error, undone_ts, undone_error, NULL FROM v1.auto_actions;")
			continue
		case "usage":
			stmts = append(stmts, "INSERT OR IGNORE INTO main.usage (id, ts, request_id, source, provider, thread_id, run_id, kind, model, effort, iterations, "+
				"input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, reasoning_tokens, searches, tool_calls, wall_ms, tier, cost_usd, "+
				"partial, unpriced, clear_edits, cleared_tokens, compaction) "+
				"SELECT id, ts, 'v1-' || id, 'loop', 'anthropic', thread_id, run_id, kind, model, effort, '[]', input_tokens, cache_write_5m, "+
				"cache_write_1h, cache_read, output_tokens, 0, searches, 0, 0, tier, cost_usd, 0, 0, clear_edits, cleared_tokens, compaction FROM v1.usage;")
			continue
		case "proposal_actions":
			stmts = append(stmts, "INSERT OR IGNORE INTO main.proposal_actions (id, proposal_id, ts, lease, kind, undo_of, action, undo, state, error) "+
				"SELECT id, proposal_id, ts, ts, CASE WHEN json_extract(action, '$.op') = 'undo' THEN 'undo' ELSE 'write' END, "+
				"CASE WHEN json_extract(action, '$.op') = 'undo' THEN json_extract(action, '$.of') END, action, undo, CASE "+
				"WHEN ok = 1 THEN 'ok' WHEN error = '"+heldText+"' THEN 'held' WHEN instr(error, '"+unknownText+"') > 0 THEN 'unknown' "+
				"WHEN instr(error, '"+partialText+"') > 0 THEN 'partial' WHEN error = '"+settledText+"' THEN 'settled' "+
				"ELSE 'failed' END, error FROM v1.proposal_actions;")
			continue
		}
		if strings.HasPrefix(table, "message_fts_") || strings.HasPrefix(table, "thread_fts_") || strings.HasPrefix(table, "notion_fts_") {
			continue
		}
		v1cols, err := columns(v1, table)
		if err != nil {
			return "", err
		}
		if len(v1cols) == 0 {
			continue
		}
		have := map[string]bool{}
		for _, c := range v1cols {
			have[c] = true
		}
		v2cols, err := columns(v2.Read, table)
		if err != nil {
			return "", err
		}
		var common []string
		for _, c := range v2cols {
			if have[c] {
				common = append(common, c)
			}
		}
		if len(common) == 0 {
			continue
		}
		quoted := make([]string, len(common))
		for i, c := range common {
			quoted[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
		}
		if ftsTables[table] {
			stmts = append(stmts, "INSERT INTO main."+table+" (rowid, "+strings.Join(quoted, ", ")+") SELECT rowid, "+
				strings.Join(quoted, ", ")+" FROM v1."+table+";")
			continue
		}
		stmts = append(stmts, "INSERT OR IGNORE INTO main."+table+" ("+strings.Join(quoted, ", ")+") SELECT "+
			strings.Join(quoted, ", ")+" FROM v1."+table+";")
	}
	stmts = append(stmts,
		"UPDATE main.proposals SET undone_ts = json_extract(result, '$.undone_ts') WHERE result IS NOT NULL AND undone_ts IS NULL;",
		"UPDATE main.proposals SET recovered = 1 WHERE json_extract(result, '$.interrupted') AND recovered = 0;")
	return "PRAGMA foreign_keys = OFF;\nATTACH DATABASE '" + strings.ReplaceAll(source, "'", "''") + "' AS v1;\n" +
		strings.Join(stmts, "\n") + "\nDETACH DATABASE v1;\n", nil
}
