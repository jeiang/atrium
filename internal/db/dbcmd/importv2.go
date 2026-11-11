package dbcmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/settings"
)

const v2Version = 11

type importAction int

const (
	carry importAction = iota
	rebuild
	convert
	drop
)

func (a importAction) String() string {
	switch a {
	case carry:
		return "carried"
	case rebuild:
		return "rebuilt"
	case convert:
		return "converted"
	default:
		return "dropped"
	}
}

type tablePlan struct {
	table  string
	action importAction
	why    string
	where  string
	args   []any
	null   []string
	skip   []string
	set    map[string]any
	ignore bool
	run    func(*importer) (int64, error)
}

var importTables = []tablePlan{
	{table: "accounts", action: carry},
	{table: "mail_accounts", action: carry, ignore: true, why: "the accounts file wins on a conflict"},
	{table: "mailboxes", action: carry},
	{table: "messages", action: carry, skip: []string{"embedded"}},
	{table: "message_fts", action: rebuild, run: func(im *importer) (int64, error) {
		return im.copyFTS("message_fts", []string{"subject", "sender", "body"})
	}},
	{table: "rules", action: carry},
	{table: "corrections", action: carry},
	{table: "decisions", action: carry},
	{table: "flag_decisions", action: carry},
	{table: "auto_actions", action: convert, run: func(im *importer) (int64, error) {
		report, err := ConvertLegacyAutoActions(im.ctx, im.src, im.tx, im.now)
		if err != nil {
			return 0, err
		}
		im.notes = append(im.notes, report.Notes()...)
		return report.Auto, nil
	}},
	{table: "write_resolutions", action: drop, why: "each row is converted into an effect resolution with the write it resolves"},
	{table: "live_state", action: carry},
	{table: "live_baseline", action: carry},
	{table: "snoozes", action: carry},
	{table: "drafts", action: carry},
	{table: "mail_scans", action: carry},
	{table: "commitments", action: carry},
	{table: "open_loops", action: carry},
	{table: "loop_overrides", action: carry},
	{table: "packages", action: carry},
	{table: "package_events", action: carry},
	{table: "watch_dismissed", action: carry},
	{table: "rebalance_candidates", action: carry},
	{table: "event_candidates", action: carry},
	{table: "settings", action: carry},
	{table: "secrets", action: carry},
	{table: "ntfy_channels", action: carry, why: "operator notification channels and topics"},
	{table: "push_subscriptions", action: carry, why: "operator device registrations; the VAPID key is carried in secrets"},
	{table: "attention_snoozes", action: carry, why: "operator snoozes keyed by carried item ids"},
	{table: "projects", action: carry},
	{table: "kb_records", action: carry, null: []string{"source_run_id"}, skip: []string{"embedded"}},
	{table: "kb_fts", action: rebuild, why: "repopulated by the kb_records triggers", run: func(im *importer) (int64, error) {
		return im.countRows("kb_fts")
	}},
	{table: "agent_profile", action: carry},
	{table: "skills", action: carry},
	{table: "subagents", action: carry, where: "builtin = 0", ignore: true, why: "built-in definitions are reseeded"},
	{table: "jobs", action: carry, null: []string{"last_run_id"}},
	{table: "threads", action: carry, null: []string{"parent_run_id", "parent_thread_id"}, run: func(im *importer) (int64, error) {
		return im.carryThreads()
	}},
	{table: "thread_messages", action: carry, null: []string{"run_id"}},
	{table: "thread_fts", action: rebuild, run: func(im *importer) (int64, error) {
		return im.copyFTS("thread_fts", []string{"thread_id", "text"})
	}},
	{table: "thread_files", action: carry, null: []string{"run_id"}},
	{table: "thread_plans", action: carry, null: []string{"run_id"}},
	{table: "thread_queue", action: carry, null: []string{"run_id"}},
	{table: "agent_events", action: carry},
	{table: "tasks", action: carry},
	{table: "proposals", action: carry},
	{table: "proposal_actions", action: convert, run: func(im *importer) (int64, error) {
		report, err := ConvertLegacyProposals(im.ctx, im.src, im.tx, im.now)
		if err != nil {
			return 0, err
		}
		im.notes = append(im.notes, report.Notes()...)
		return report.Proposal, nil
	}},
	{table: "agent_writes", action: convert, run: func(im *importer) (int64, error) {
		report, err := ConvertLegacyAgentWrites(im.ctx, im.src, im.tx, im.now)
		if err != nil {
			return 0, err
		}
		im.notes = append(im.notes, report.Notes()...)
		return report.Agent, nil
	}},
	{table: "media_channels", action: carry},
	{table: "media_seen", action: carry},
	{table: "media_state", action: carry},
	{table: "usage", action: carry, run: func(im *importer) (int64, error) {
		return im.carryUsage()
	}},
	{table: "embed_index", action: rebuild, why: "the active index row and its message vectors are carried", run: func(im *importer) (int64, error) {
		return im.carryIndex()
	}},

	{table: "runs", action: drop, why: "run history is not carried"},
	{table: "run_events", action: drop, why: "run history is not carried"},
	{table: "tool_calls", action: drop, why: "run history is not carried"},
	{table: "tool_results", action: drop, why: "run history is not carried"},
	{table: "context_ledger", action: drop, why: "run history is not carried"},
	{table: "context_projections", action: drop, why: "run history is not carried"},
	{table: "memory_nodes", action: drop, why: "memory is rebuilt from the carried messages"},
	{table: "memory_jobs", action: drop, why: "memory is rebuilt from the carried messages"},
	{table: "memory_views", action: drop, why: "memory is rebuilt from the carried messages"},
	{table: "memory_state", action: drop, why: "memory is rebuilt from the carried messages"},
	{table: "memory_fts", action: drop, why: "memory is rebuilt from the carried messages"},
	{table: "notion_pages", action: drop, why: "Notion re-syncs from the provider"},
	{table: "notion_sources", action: drop, why: "Notion re-syncs from the provider"},
	{table: "notion_state", action: drop, why: "Notion re-syncs from the provider"},
	{table: "notion_fts", action: drop, why: "Notion re-syncs from the provider"},
	{table: "wiki_pages", action: drop, why: "the wiki re-syncs from Notion"},
	{table: "export_pages", action: drop, why: "exports are rebuilt"},
	{table: "notifications", action: drop, why: "the delivery log is not carried"},
	{table: "auth_sessions", action: drop, why: "sessions do not survive the import"},
	{table: "auth_logins", action: drop, why: "in-flight sign-ins do not survive the import"},
	{table: "auth_login_links", action: drop, why: "login links do not survive the import"},
	{table: "oauth_states", action: drop, why: "transient OAuth handshakes do not survive the import"},
	{table: "action_links", action: drop, why: "signed action links expire"},
	{table: "settings_audit", action: drop, why: "the destination's audit starts fresh"},
	{table: "job_runs", action: drop, why: "job history is not carried; the jobs are"},
	{table: "events", action: drop, why: "the mail event log is not carried"},
	{table: "pass_runs", action: drop, why: "ops pass history is not carried"},
	{table: "pass_run_alerts", action: drop, why: "ops pass history is not carried"},
	{table: "endpoint_probes", action: drop, why: "endpoints are re-probed"},
	{table: "integration_health", action: drop, why: "integrations are re-probed"},
	{table: "ops_samples", action: drop, why: "ops samples are not carried"},
	{table: "ops_findings", action: drop, why: "ops findings are not carried"},
	{table: "nightly_observations", action: drop, why: "nightly observations are not carried"},
	{table: "db_key_history", action: drop, why: "key history belongs to the destination's key lineage; old backups are read with the old key"},
	{table: "module_migrations", action: drop, why: "v3 has no modules"},
	{table: "schema_migrations", action: drop, why: "the destination records its own schema version"},
}

var ftsParents = []string{"message_fts", "thread_fts", "kb_fts", "notion_fts", "memory_fts"}

var ftsShadows = []string{"data", "idx", "content", "docsize", "config"}

var vecShadows = []string{"info", "chunks", "rowids", "vector_chunks00"}

type importer struct {
	ctx     context.Context
	src     *sql.DB
	tx      *sql.Tx
	now     float64
	loc     *time.Location
	vectors int64
	notes   []string
}

func (s *service) importV2(a *cli.Args) cli.Handler {
	source := a.Arg("path", cli.Opt{Help: "path to the v2 database to import into this fresh v3 database"})
	return func(e *cli.Env) int {
		cfg, err := config.Load(s.env)
		if err != nil {
			return cli.Fail(e, err)
		}
		key, err := s.currentKey(e)
		if err != nil {
			return cli.Fail(e, err)
		}
		lines, err := importV2(e.Ctx, s.env, cfg, *source, key, s.clock())
		if err != nil {
			return cli.Fail(e, err)
		}
		for _, line := range lines {
			_, _ = fmt.Fprintln(e.Stdout, line)
		}
		return cli.ExitOK
	}
}

func (s *service) clock() float64 {
	if s.now != nil {
		return s.now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func importV2(ctx context.Context, env config.Env, cfg config.Config, source string, key db.Key, now float64) ([]string, error) {
	if sameFile(source, cfg.DBPath) {
		return nil, errors.New("db import-v2: the source and the destination are the same file")
	}
	src, err := db.OpenSource(source, key)
	if err != nil {
		return nil, fmt.Errorf("db import-v2: source %s: %w", source, err)
	}
	defer func() { _ = src.Close() }()
	plans, err := inspectSource(ctx, src, now)
	if err != nil {
		return nil, err
	}
	if err := refuseDestination(ctx, cfg.DBPath, key); err != nil {
		return nil, err
	}
	d, err := db.Connect(cfg.DBPath, db.WithKey(key))
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	if err := config.Boot(ctx, env, d, cfg); err != nil {
		return nil, err
	}
	loc, err := importLocation(ctx, src, env)
	if err != nil {
		return nil, err
	}
	var lines []string
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		im := &importer{ctx: ctx, src: src, tx: tx, now: now, loc: loc}
		for _, plan := range plans {
			n, err := im.apply(plan)
			if err != nil {
				return fmt.Errorf("db import-v2: %s: %w", plan.table, err)
			}
			line := fmt.Sprintf("%s: %s %d", plan.table, plan.action, n)
			if plan.why != "" {
				line += " (" + plan.why + ")"
			}
			lines = append(lines, line)
			for _, note := range im.notes {
				lines = append(lines, plan.table+": "+note)
			}
			im.notes = nil
		}
		return im.checkForeignKeys()
	})
	if err != nil {
		return nil, err
	}
	unknown, err := unknownSettings(ctx, src)
	if err != nil {
		return nil, err
	}
	for _, name := range unknown {
		lines = append(lines, fmt.Sprintf("setting not known to v3: %s (carried as-is)", name))
	}
	return lines, nil
}

func inspectSource(ctx context.Context, src *sql.DB, now float64) ([]tablePlan, error) {
	var tables int
	if err := src.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return nil, err
	}
	if tables == 0 {
		return nil, errors.New("db import-v2: the source has no tables; it is not a v2 database")
	}
	if err := db.RequireV3(ctx, src); err == nil {
		return nil, errors.New("db import-v2: the source already has the v3 marker; import only a v2 database")
	} else if !errors.Is(err, db.ErrNotV3) {
		return nil, err
	}
	var version sql.NullInt64
	if err := src.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		return nil, fmt.Errorf("db import-v2: the source has no schema_migrations table; it is not a v2 database: %w", err)
	}
	if !version.Valid || version.Int64 != v2Version {
		return nil, fmt.Errorf("db import-v2: the source is at v2 schema version %d; this build imports version %d",
			version.Int64, v2Version)
	}
	var building int64
	if err := src.QueryRowContext(ctx, "SELECT COUNT(*) FROM embed_index WHERE state = 'building'").Scan(&building); err != nil {
		return nil, fmt.Errorf("db import-v2: the source has no embed_index table; it is not a v2 database: %w", err)
	}
	if building > 0 {
		return nil, errors.New("db import-v2: the source has an embedding index rebuild in progress; let it finish or discard it before importing")
	}
	active, err := embed.Active(ctx, src)
	if err != nil {
		return nil, fmt.Errorf("db import-v2: source: %w", err)
	}
	if err := refuseInFlight(ctx, src, now); err != nil {
		return nil, err
	}
	names, err := sourceTables(ctx, src)
	if err != nil {
		return nil, err
	}
	return planTables(names, active)
}

func refuseInFlight(ctx context.Context, src *sql.DB, now float64) error {
	checks := []struct {
		table string
		query string
		args  []any
	}{
		{"auto_actions", "SELECT COUNT(*) FROM auto_actions WHERE (state = 'held' AND error IS NULL) OR (state = 'undo_held' AND undone_error IS NULL)", nil},
		{"proposal_actions", "SELECT COUNT(*) FROM proposal_actions WHERE state = 'held' AND error IS NULL AND lease >= ?", []any{now}},
		{"drafts", "SELECT COUNT(*) FROM drafts WHERE status = 'sending'", nil},
	}
	var parts []string
	for _, check := range checks {
		var n int64
		if err := src.QueryRowContext(ctx, check.query, check.args...).Scan(&n); err != nil {
			return fmt.Errorf("db import-v2: %s: %w", check.table, err)
		}
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", check.table, n))
		}
	}
	if len(parts) > 0 {
		return fmt.Errorf("db import-v2: the source has writes in flight that cannot be carried safely (%s); "+
			"stop v2 and resolve them first, then import again", strings.Join(parts, ", "))
	}
	return nil
}

func sourceTables(ctx context.Context, src *sql.DB) ([]string, error) {
	rows, err := src.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var name string
		return name, r.Scan(&name)
	})
}

func planTables(names []string, active embed.Index) ([]tablePlan, error) {
	present := make(map[string]bool, len(names))
	vecParents := map[string]bool{}
	for _, name := range names {
		present[name] = true
		if _, _, ok := splitVecTable(name); ok {
			vecParents[name] = true
		}
	}
	var dynamic []tablePlan
	var unknown []string
	for _, name := range names {
		if slices.ContainsFunc(importTables, func(plan tablePlan) bool { return plan.table == name }) {
			continue
		}
		if kind, generation, ok := splitVecTable(name); ok {
			switch {
			case kind == "message" && generation == active.Generation:
				dynamic = append(dynamic, tablePlan{table: name, action: carry, run: func(im *importer) (int64, error) {
					return im.vectors, nil
				}})
			case kind == "message":
				dynamic = append(dynamic, tablePlan{table: name, action: drop, why: "stale embedding generation"})
			case kind == "notion":
				dynamic = append(dynamic, tablePlan{table: name, action: drop, why: "Notion re-syncs and re-embeds"})
			default:
				dynamic = append(dynamic, tablePlan{table: name, action: drop, why: "knowledge vectors rebuild from the carried records"})
			}
			continue
		}
		if isShadow(name, vecParents) {
			continue
		}
		unknown = append(unknown, name)
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("db import-v2: the source has tables this build does not know: %s; nothing was imported",
			strings.Join(unknown, ", "))
	}
	for _, plan := range importTables {
		if !present[plan.table] {
			return nil, fmt.Errorf("db import-v2: the source has no %s table; it is not a v2 database at version %d",
				plan.table, v2Version)
		}
	}
	return append(slices.Clone(importTables), dynamic...), nil
}

func splitVecTable(name string) (kind string, generation int64, ok bool) {
	for _, prefix := range []string{"embed_message_", "embed_notion_", "embed_kb_"} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		generation, err := strconv.ParseInt(strings.TrimPrefix(name, prefix), 10, 64)
		if err != nil {
			return "", 0, false
		}
		return strings.TrimSuffix(strings.TrimPrefix(prefix, "embed_"), "_"), generation, true
	}
	return "", 0, false
}

func isShadow(name string, vecParents map[string]bool) bool {
	for _, parent := range ftsParents {
		if strings.HasPrefix(name, parent+"_") && slices.Contains(ftsShadows, strings.TrimPrefix(name, parent+"_")) {
			return true
		}
	}
	for parent := range vecParents {
		if strings.HasPrefix(name, parent+"_") && slices.Contains(vecShadows, strings.TrimPrefix(name, parent+"_")) {
			return true
		}
	}
	return false
}

func (im *importer) apply(plan tablePlan) (int64, error) {
	if plan.action == drop {
		return im.countSource(plan.table)
	}
	if plan.run != nil {
		return plan.run(im)
	}
	return im.copy(plan.table, plan.where, plan.args, plan.null, plan.skip, plan.set, plan.ignore)
}

func (im *importer) copy(table, where string, args []any, null, skip []string, set map[string]any, ignore bool) (int64, error) {
	source, err := tableColumns(im.ctx, im.src, table)
	if err != nil {
		return 0, err
	}
	dest, err := tableColumns(im.ctx, im.tx, table)
	if err != nil {
		return 0, err
	}
	columns := make([]string, 0, len(source))
	for _, column := range source {
		if slices.Contains(skip, column) {
			continue
		}
		if !slices.Contains(dest, column) {
			return 0, fmt.Errorf("the destination %s table has no %s column; update the importer for this schema change", table, column)
		}
		columns = append(columns, column)
	}
	query := "SELECT " + quoteList(columns) + " FROM " + quoteName(table)
	if where != "" {
		query += " WHERE " + where
	}
	rows, err := im.src.QueryContext(im.ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	verb := "INSERT INTO "
	if ignore {
		verb = "INSERT OR IGNORE INTO "
	}
	insert := verb + quoteName(table) + " (" + quoteList(columns) + ") VALUES (" + placeholders(len(columns)) + ")"
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	var n int64
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return n, err
		}
		for _, column := range null {
			if i := slices.Index(columns, column); i >= 0 {
				values[i] = nil
			}
		}
		for column, value := range set {
			if i := slices.Index(columns, column); i >= 0 {
				values[i] = value
			}
		}
		res, err := im.tx.ExecContext(im.ctx, insert, values...)
		if err != nil {
			return n, err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return n, err
		}
		n += changed
	}
	return n, rows.Err()
}

func (im *importer) copyFTS(table string, columns []string) (int64, error) {
	rows, err := im.src.QueryContext(im.ctx, "SELECT rowid, "+quoteList(columns)+" FROM "+quoteName(table))
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	insert := "INSERT INTO " + quoteName(table) + " (rowid, " + quoteList(columns) + ") VALUES (" + placeholders(len(columns)+1) + ")"
	values := make([]any, len(columns)+1)
	pointers := make([]any, len(columns)+1)
	for i := range values {
		pointers[i] = &values[i]
	}
	var n int64
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return n, err
		}
		if _, err := im.tx.ExecContext(im.ctx, insert, values...); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func (im *importer) carryThreads() (int64, error) {
	n, err := im.copy("threads", "", nil, []string{"parent_thread_id", "parent_run_id"}, nil, nil, false)
	if err != nil {
		return n, err
	}
	rows, err := im.src.QueryContext(im.ctx, "SELECT id, parent_thread_id FROM threads WHERE parent_thread_id IS NOT NULL")
	if err != nil {
		return n, err
	}
	links, err := db.Collect(rows, func(r *sql.Rows) ([2]int64, error) {
		var link [2]int64
		err := r.Scan(&link[0], &link[1])
		return link, err
	})
	if err != nil {
		return n, err
	}
	for _, link := range links {
		if _, err := im.tx.ExecContext(im.ctx, "UPDATE threads SET parent_thread_id = ? WHERE id = ?", link[1], link[0]); err != nil {
			return n, err
		}
	}
	return n, nil
}

func (im *importer) carryIndex() (int64, error) {
	source, err := embed.Active(im.ctx, im.src)
	if err != nil {
		return 0, err
	}
	dest, err := embed.Active(im.ctx, im.tx)
	if err != nil {
		return 0, err
	}
	table := dest.MessageTable()
	if source.Dimension != dest.Dimension {
		var generation int64
		if err := im.tx.QueryRowContext(im.ctx, "SELECT COALESCE(MAX(generation), 0) + 1 FROM embed_index").Scan(&generation); err != nil {
			return 0, err
		}
		for _, stmt := range []string{
			fmt.Sprintf("CREATE VIRTUAL TABLE embed_message_%d USING vec0(account TEXT PARTITION KEY, embedding FLOAT[%d] DISTANCE_METRIC=cosine)", generation, source.Dimension),
			fmt.Sprintf("CREATE VIRTUAL TABLE embed_notion_%d USING vec0(visibility TEXT PARTITION KEY, embedding FLOAT[%d] DISTANCE_METRIC=cosine)", generation, source.Dimension),
			fmt.Sprintf("CREATE VIRTUAL TABLE embed_kb_%d USING vec0(embedding FLOAT[%d] DISTANCE_METRIC=cosine)", generation, source.Dimension),
		} {
			if _, err := im.tx.ExecContext(im.ctx, stmt); err != nil {
				return 0, err
			}
		}
		for _, stmt := range []string{
			"DROP TABLE IF EXISTS " + dest.MessageTable(),
			"DROP TABLE IF EXISTS " + dest.NotionTable(),
			"DROP TABLE IF EXISTS " + dest.KBTable(),
			"DELETE FROM embed_index WHERE generation = " + strconv.FormatInt(dest.Generation, 10),
		} {
			if _, err := im.tx.ExecContext(im.ctx, stmt); err != nil {
				return 0, err
			}
		}
		if _, err := im.tx.ExecContext(im.ctx,
			"INSERT INTO embed_index (generation, state, url, model, dimension, done, total, error) VALUES (?, 'active', ?, ?, ?, ?, ?, ?)",
			generation, source.URL, source.Model, source.Dimension, source.Done, source.Total, source.Error); err != nil {
			return 0, err
		}
		table = fmt.Sprintf("embed_message_%d", generation)
	} else if _, err := im.tx.ExecContext(im.ctx,
		"UPDATE embed_index SET url = ?, model = ?, dimension = ?, done = ?, total = ?, error = ? WHERE generation = ?",
		source.URL, source.Model, source.Dimension, source.Done, source.Total, source.Error, dest.Generation); err != nil {
		return 0, err
	}
	rows, err := im.src.QueryContext(im.ctx, "SELECT rowid, account, embedding FROM "+quoteName(source.MessageTable()))
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	insert := "INSERT INTO " + quoteName(table) + " (rowid, account, embedding) VALUES (?, ?, ?)"
	var n int64
	for rows.Next() {
		var rowid int64
		var account string
		var vector []byte
		if err := rows.Scan(&rowid, &account, &vector); err != nil {
			return n, err
		}
		if _, err := im.tx.ExecContext(im.ctx, insert, rowid, account, vector); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	if err := im.carryMessageHashes(table); err != nil {
		return n, err
	}
	im.vectors = n
	return n, nil
}

func (im *importer) carryUsage() (int64, error) {
	var future int64
	if err := im.src.QueryRowContext(im.ctx, "SELECT COUNT(*) FROM usage WHERE ts > ?", im.now).Scan(&future); err != nil {
		return 0, err
	}
	if future > 0 {
		return 0, fmt.Errorf("%d usage rows are dated after the import instant; stop v2 and import again once they are in the past", future)
	}
	month := inference.MonthStart(im.now, im.loc)
	next := inference.NextMonthStart(im.now, im.loc)
	rows, err := im.src.QueryContext(im.ctx,
		"SELECT id, ts, request_id, source, provider, thread_id, kind, job_name, model, effort, iterations, input_tokens, "+
			"cache_write_5m, cache_write_1h, cache_read, output_tokens, reasoning_tokens, searches, tool_calls, wall_ms, tier, "+
			"cost_usd, partial, unpriced, clear_edits, cleared_tokens, compaction FROM usage WHERE ts >= ? AND ts < ? ORDER BY id",
		month, next)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var n int64
	for rows.Next() {
		var e inference.ImportedEntry
		var threadID sql.NullInt64
		var jobName sql.NullString
		var partial, unpriced int64
		if err := rows.Scan(&e.ID, &e.TS, &e.RequestID, &e.Source, &e.Provider, &threadID, &e.Kind, &jobName, &e.Model, &e.Effort,
			&e.Iterations, &e.InputTokens, &e.CacheWrite5m, &e.CacheWrite1h, &e.CacheRead, &e.OutputTokens, &e.ReasoningTokens,
			&e.Searches, &e.ToolCalls, &e.WallMS, &e.Tier, &e.CostUSD, &partial, &unpriced, &e.ClearEdits, &e.ClearedTokens,
			&e.Compaction); err != nil {
			return n, err
		}
		if e.TS > im.now {
			return n, fmt.Errorf("usage row %d is dated after the import instant", e.ID)
		}
		if threadID.Valid {
			e.ThreadID = threadID.Int64
		}
		if jobName.Valid {
			e.JobName = jobName.String
		}
		e.Partial = partial != 0
		e.Unpriced = unpriced != 0
		e.MonthStart = month
		if err := inference.ImportSettled(im.ctx, im.tx, e); err != nil {
			return n, fmt.Errorf("usage row %d: %w", e.ID, err)
		}
		n++
	}
	return n, rows.Err()
}

func (im *importer) carryMessageHashes(table string) error {
	ctx, err := embed.WithModels(im.ctx, im.tx)
	if err != nil {
		return err
	}
	rows, err := im.tx.QueryContext(im.ctx,
		"SELECT m.id, m.subject, m.from_name, m.from_addr, f.body FROM messages m JOIN message_fts f ON f.rowid = m.id "+
			"WHERE m.id IN (SELECT rowid FROM "+quoteName(table)+")")
	if err != nil {
		return err
	}
	type carried struct {
		id   int64
		hash string
	}
	list, err := db.Collect(rows, func(r *sql.Rows) (carried, error) {
		var c carried
		var subject, name, addr, body sql.NullString
		if err := r.Scan(&c.id, &subject, &name, &addr, &body); err != nil {
			return carried{}, err
		}
		c.hash = embed.Hash(embed.DocumentText(ctx, subject.String, name.String, addr.String, body.String))
		return c, nil
	})
	if err != nil {
		return err
	}
	for _, c := range list {
		if _, err := im.tx.ExecContext(im.ctx, "UPDATE messages SET embedded_hash = ? WHERE id = ?", c.hash, c.id); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) countSource(table string) (int64, error) {
	var n int64
	err := im.src.QueryRowContext(im.ctx, "SELECT COUNT(*) FROM "+quoteName(table)).Scan(&n)
	return n, err
}

func (im *importer) countRows(table string) (int64, error) {
	var n int64
	err := im.tx.QueryRowContext(im.ctx, "SELECT COUNT(*) FROM "+quoteName(table)).Scan(&n)
	return n, err
}

func (im *importer) checkForeignKeys() error {
	rows, err := im.tx.QueryContext(im.ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return rows.Err()
	}
	var table, parent string
	var rowid int64
	var fkid int
	if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
		return err
	}
	return fmt.Errorf("db import-v2: foreign key check failed: %s row %d references %s", table, rowid, parent)
}

func refuseDestination(ctx context.Context, path string, key db.Key) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	q, err := db.OpenSource(path, key)
	if err != nil {
		return fmt.Errorf("db import-v2: destination %s: %w", path, err)
	}
	defer func() { _ = q.Close() }()
	if err := db.RequireV3(ctx, q); err != nil {
		if errors.Is(err, db.ErrNotV3) {
			return fmt.Errorf("db import-v2: the destination %s is not a v3 database; point the configured database at a fresh path", path)
		}
		return err
	}
	var counts []string
	for _, plan := range importTables {
		if plan.action != carry || plan.table == "mail_accounts" || plan.table == "subagents" {
			continue
		}
		var n int64
		if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteName(plan.table)).Scan(&n); err != nil {
			return fmt.Errorf("db import-v2: destination: %w", err)
		}
		if n > 0 {
			counts = append(counts, fmt.Sprintf("%s=%d", plan.table, n))
		}
	}
	var n int64
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM subagents WHERE builtin = 0").Scan(&n); err != nil {
		return fmt.Errorf("db import-v2: destination: %w", err)
	}
	if n > 0 {
		counts = append(counts, fmt.Sprintf("subagents=%d", n))
	}
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM effects").Scan(&n); err != nil {
		return fmt.Errorf("db import-v2: destination: %w", err)
	}
	if n > 0 {
		counts = append(counts, fmt.Sprintf("effects=%d", n))
	}
	if active, err := embed.Active(ctx, q); err == nil {
		if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteName(active.MessageTable())).Scan(&n); err != nil {
			return fmt.Errorf("db import-v2: destination: %w", err)
		}
		if n > 0 {
			counts = append(counts, fmt.Sprintf("%s=%d", active.MessageTable(), n))
		}
	}
	if len(counts) > 0 {
		return fmt.Errorf("db import-v2: the destination database already holds rows (%s); import into a fresh database",
			strings.Join(counts, ", "))
	}
	return nil
}

func importLocation(ctx context.Context, src *sql.DB, env config.Env) (*time.Location, error) {
	return inference.Location(ctx, src)
}

func unknownSettings(ctx context.Context, src *sql.DB) ([]string, error) {
	rows, err := src.QueryContext(ctx, "SELECT key FROM settings ORDER BY key")
	if err != nil {
		return nil, err
	}
	keys, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var key string
		return key, r.Scan(&key)
	})
	if err != nil {
		return nil, err
	}
	reg := settings.From(ctx)
	var unknown []string
	for _, key := range keys {
		if _, ok := reg.Lookup(key); !ok {
			unknown = append(unknown, key)
		}
	}
	return unknown, nil
}

func tableColumns(ctx context.Context, q db.Querier, table string) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var name string
		return name, r.Scan(&name)
	})
}

func quoteName(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quoteName(name)
	}
	return strings.Join(quoted, ", ")
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func sameFile(a, b string) bool {
	left, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	right, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	if left == right {
		return true
	}
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false
	}
	return os.SameFile(leftInfo, rightInfo)
}
