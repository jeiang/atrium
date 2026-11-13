package importv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/anthropicconn"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/auth/oidcconn"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/weather"
)

const v2Version = 11

type importAction int

const (
	carry importAction = iota
	rebuild
	convert
	drop
	fresh
	derive
)

func (a importAction) String() string {
	switch a {
	case carry:
		return "carried"
	case rebuild:
		return "rebuilt"
	case convert:
		return "converted"
	case fresh:
		return "kept empty"
	case derive:
		return "derived"
	default:
		return "dropped"
	}
}

type tablePlan struct {
	table  string
	action importAction
	why    string
	note   string
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
	{table: "mail_accounts", action: carry, ignore: true, why: "a host-declared account keeps its identity; the import supplies its credential"},
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
	{table: "push_subscriptions", action: convert, why: "each valid registration becomes a web push device with the same id; malformed rows are reported and not carried; the VAPID key is carried in secrets", run: func(im *importer) (int64, error) {
		n, notes, err := notify.ImportDevices(im.ctx, im.src, im.tx)
		im.notes = append(im.notes, notes...)
		return n, err
	}},
	{table: "attention_snoozes", action: carry, why: "operator snoozes keyed by carried item ids"},
	{table: "projects", action: carry},
	{table: "kb_records", action: carry, null: []string{"source_run_id"}, skip: []string{"embedded"}},
	{table: "kb_fts", action: rebuild, why: "repopulated by the kb_records triggers", run: func(im *importer) (int64, error) {
		return im.countRows("kb_fts")
	}},
	{table: "agent_profile", action: carry},
	{table: "skills", action: carry},
	{table: "subagents", action: carry, where: "builtin = 0", ignore: true, why: "built-in definitions are reseeded"},
	{table: "jobs", action: convert, why: "declared jobs are keyed by owner; leases and run links are cleared", run: func(im *importer) (int64, error) {
		return im.jobs()
	}},
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
	{table: "run_streams", action: fresh, why: "run history is not carried"},
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
	{table: "settings_audit", action: carry, why: "historical audit is carried unchanged before the import's own sanitized entries", run: func(im *importer) (int64, error) {
		return im.carryAudit()
	}},
	{table: "job_runs", action: drop, why: "job history is not carried; the jobs are"},
	{table: "job_run_threads", action: fresh, why: "new in v3; job history is not carried"},
	{table: "job_resource_leases", action: fresh, why: "new in v3; no claim survives an import"},
	{table: "operation_audit", action: fresh, why: "new in v3; no operation history is made up from imported rows"},
	{table: "commands", action: fresh, why: "new in v3; no command history is carried"},
	{table: "events", action: drop, why: "the mail event log is not carried"},
	{table: "pass_runs", action: drop, why: "ops pass history is not carried"},
	{table: "pass_run_alerts", action: drop, why: "ops pass history is not carried"},
	{table: "endpoint_probes", action: drop, why: "the chat endpoint is removed; the embed probe moves to connection service/embedding", run: func(im *importer) (int64, error) {
		var chat, moved int64
		if err := im.src.QueryRowContext(im.ctx, "SELECT COUNT(*) FILTER (WHERE kind = 'chat'), COUNT(*) FILTER (WHERE kind = 'embed') FROM endpoint_probes").Scan(&chat, &moved); err != nil {
			return 0, err
		}
		if chat > 0 {
			im.notes = append(im.notes, fmt.Sprintf("drop-removed chat probe: %d row reason=Haiku-classification", chat))
		}
		if moved > 0 {
			im.notes = append(im.notes, "the embed probe moved to connection service/embedding")
		}
		return im.countSource("endpoint_probes")
	}},
	{table: "integration_health", action: drop, why: "connection health lives in the connections table", run: func(im *importer) (int64, error) {
		var chat int64
		if err := im.src.QueryRowContext(im.ctx, "SELECT COUNT(*) FROM integration_health WHERE name = 'mail_chat'").Scan(&chat); err != nil {
			return 0, err
		}
		if chat > 0 {
			im.notes = append(im.notes, fmt.Sprintf("drop-removed mail_chat health: %d row reason=Haiku-classification", chat))
		}
		return im.countSource("integration_health")
	}},
	{table: "ops_samples", action: drop, why: "ops samples are not carried"},
	{table: "ops_findings", action: drop, why: "ops findings are not carried"},
	{table: "nightly_observations", action: drop, why: "nightly observations are not carried"},
	{table: "db_key_history", action: drop, why: "key history belongs to the destination's key lineage; old backups are read with the old key"},
	{table: "module_migrations", action: drop, why: "v3 has no modules"},
	{table: "schema_migrations", action: drop, why: "the destination records its own schema version"},
	{table: "read_revisions", action: derive, why: "new in v3; read revision scopes counted by the destination as rows arrive, none taken from the source"},
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

func Run(ctx context.Context, env config.Env, cfg config.Config, source, sourceConfig, sourceEnv string, key db.Key, now float64, commit func(*sql.Tx, int) error) ([]string, error) {
	if sameFile(source, cfg.DBPath) {
		return nil, errors.New("db import-v2: the source and the destination are the same file")
	}
	pins, err := readSourcePins(env, sourceConfig, sourceEnv)
	if err != nil {
		return nil, err
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
	if _, _, err := sourceICloudSelection(ctx, src, pins); err != nil {
		return nil, err
	}
	exclusion, err := db.TryServeLock(ctx, cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("db import-v2 refused: %w", err)
	}
	defer func() { _ = exclusion.Close() }()
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
			if plan.note != "" && n > 0 {
				im.notes = append(im.notes, plan.note)
			}
			for _, note := range im.notes {
				lines = append(lines, plan.table+": "+note)
			}
			im.notes = nil
		}
		settingLines, summary, err := settings.ImportV2(ctx, tx, src, pins.importPins())
		if err != nil {
			return err
		}
		lines = append(lines, settingLines...)
		scheduleLines, err := scheduler.FinalizeImportedSchedules(ctx, tx, jobDefinitions(ctx), now)
		if err != nil {
			return err
		}
		lines = append(lines, scheduleLines...)
		for _, name := range pins.unknown {
			lines = append(lines, "pin "+name+": unknown-host-requirement")
		}
		accounts, err := importedAccountIDs(ctx, tx)
		if err != nil {
			return err
		}
		sourced, err := sourceConnections(ctx, src, accounts, pins)
		if err != nil {
			return err
		}
		connLines, err := connections.ImportV2(ctx, tx, sourced.Qbit, sourced.Services, d.Encrypted(), now)
		if err != nil {
			return err
		}
		lines = append(lines, connLines...)
		lines = append(lines, sourced.Notes...)
		host := config.From(ctx).Host()
		for _, id := range sourced.MailIDs {
			pinned := 0
			if c, ok := host.Connection(id); ok && c.Kind == "mail" {
				pinned = 1
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE mail_accounts SET connection_id = ?, capability_prefix = ?, pinned = ? WHERE id = ?",
				id, id, pinned, strings.TrimPrefix(id, "mail/")); err != nil {
				return err
			}
			lines = append(lines, "mail_accounts: linked "+id)
		}
		secretLines, err := im.accountSecrets(sourced.Secrets, pins)
		if err != nil {
			return err
		}
		lines = append(lines, secretLines...)
		lines = append(lines, summary.Lines()...)
		if err := im.checkForeignKeys(); err != nil {
			return err
		}
		return commit(tx, len(lines))
	})
	if err != nil {
		return nil, err
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
	if err := refuseUnknownSecrets(ctx, src); err != nil {
		return nil, err
	}
	names, err := sourceTables(ctx, src)
	if err != nil {
		return nil, err
	}
	return planTables(names, active)
}

func refuseUnknownSecrets(ctx context.Context, src *sql.DB) error {
	accounts, err := sourceAccountIDs(ctx, src)
	if err != nil {
		return err
	}
	rows, err := src.QueryContext(ctx, "SELECT name, value FROM secrets ORDER BY name")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return err
		}
		if value == "" || knownSourceSecret(name, accounts) {
			continue
		}
		return fmt.Errorf("db import-v2: the source has a secret this build does not know: %s; nothing was imported", name)
	}
	return rows.Err()
}

func sourceAccountIDs(ctx context.Context, src *sql.DB) (map[string]bool, error) {
	rows, err := src.QueryContext(ctx, "SELECT id FROM mail_accounts")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func knownSourceSecret(name string, accounts map[string]bool) bool {
	if settings.ImportSecretName(name) {
		return true
	}
	id, ok := strings.CutPrefix(name, "mail_password_")
	return ok && accounts[id]
}

func (im *importer) accountSecrets(consumed []string, pins *sourcePins) ([]string, error) {
	accounts, err := sourceAccountIDs(im.ctx, im.src)
	if err != nil {
		return nil, err
	}
	moved := make(map[string]bool, len(consumed))
	for _, name := range consumed {
		moved[name] = true
	}
	rows, err := im.src.QueryContext(im.ctx, "SELECT name, value FROM secrets ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		if entry, ok := settings.ImportEntryFor(name); ok && entry.Action == settings.ImportDrop {
			if _, err := im.tx.ExecContext(im.ctx, "DELETE FROM secrets WHERE name = ?", name); err != nil {
				return nil, err
			}
			lines = append(lines, "secret "+name+": dropped -> "+entry.Target+" (value not kept)")
			continue
		}
		if _, pinned := pins.m[name]; pinned {
			if _, err := im.tx.ExecContext(im.ctx, "DELETE FROM secrets WHERE name = ?", name); err != nil {
				return nil, err
			}
			target := "host credential"
			if entry, ok := settings.ImportEntryFor(name); ok && entry.Target != "" {
				target = entry.Target
			}
			lines = append(lines, "secret "+name+": drop-shadowed-row (the effective pin wins)")
			lines = append(lines, "secret "+name+": credential-file-binding "+target)
			continue
		}
		if moved[name] {
			if _, err := im.tx.ExecContext(im.ctx, "DELETE FROM secrets WHERE name = ?", name); err != nil {
				return nil, err
			}
			target := "its connection credential"
			if entry, ok := settings.ImportEntryFor(name); ok {
				target = entry.Target
			} else if id, ok := strings.CutPrefix(name, "mail_password_"); ok {
				target = "mail/" + id + "/password"
			}
			lines = append(lines, "secret "+name+": moved -> "+target)
			continue
		}
		if value == "" {
			if _, err := im.tx.ExecContext(im.ctx, "DELETE FROM secrets WHERE name = ?", name); err != nil {
				return nil, err
			}
			lines = append(lines, "secret "+name+": dropped (empty)")
			continue
		}
		if !knownSourceSecret(name, accounts) {
			return nil, fmt.Errorf("db import-v2: the source has a secret this build does not know: %s; nothing was imported", name)
		}
		entry, ok := settings.ImportEntryFor(name)
		if !ok || entry.Action == settings.ImportCarry {
			continue
		}
		if _, err := im.tx.ExecContext(im.ctx, "DELETE FROM secrets WHERE name = ?", name); err != nil {
			return nil, err
		}
		switch entry.Class {
		case settings.ImportHost:
			lines = append(lines, "secret "+name+": drop-host -> "+entry.Target+" (secret; value not printed)")
		default:
			lines = append(lines, "secret "+name+": dropped -> "+entry.Target+" (value not kept)")
		}
	}
	return lines, rows.Err()
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

func sourceQbit(ctx context.Context, src *sql.DB, pins *sourcePins) (connections.ImportQbit, error) {
	var q connections.ImportQbit
	values, err := sourceSettings(ctx, src, pins, []string{"media_qbittorrent_url", "qbittorrent_ca_file", "qbittorrent_proxy"})
	if err != nil {
		return q, err
	}
	q.URL = values["media_qbittorrent_url"]
	q.CAFile = values["qbittorrent_ca_file"]
	q.Proxy = values["qbittorrent_proxy"]
	key, err := sourceSecret(ctx, src, pins, "media_qbittorrent_api_key", "api_key")
	if err != nil {
		return q, err
	}
	q.Key, q.SetAt, q.SetBy = key.Value, key.SetAt, key.SetBy
	var state, reason string
	var checked float64
	var expires sql.NullFloat64
	err = src.QueryRowContext(ctx, "SELECT state, reason, checked_at, expires_at FROM integration_health WHERE name = 'qbittorrent'").
		Scan(&state, &reason, &checked, &expires)
	if err == nil {
		q.Health = &connections.ImportHealth{State: state, Reason: reason, CheckedAt: checked}
		if expires.Valid {
			q.Health.ExpiresAt = &expires.Float64
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return q, err
	}
	return q, nil
}

type sourceConnectionsData struct {
	Qbit     connections.ImportQbit
	Services []connections.ImportService
	Secrets  []string
	Notes    []string
	MailIDs  []string
}

func importedAccountIDs(ctx context.Context, q *sql.Tx) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT id FROM mail_accounts")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[id] = true
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

func sourceConnections(ctx context.Context, src *sql.DB, accounts map[string]bool, pins *sourcePins) (sourceConnectionsData, error) {
	var out sourceConnectionsData
	qbit, err := sourceQbit(ctx, src, pins)
	if err != nil {
		return out, err
	}
	out.Qbit = qbit
	if qbit.Key != "" {
		out.Secrets = append(out.Secrets, "media_qbittorrent_api_key")
	}
	values, err := sourceSettings(ctx, src, pins, []string{
		"anthropic_ca_file", "anthropic_proxy",
		"grafana_url", "grafana_metrics_uid", "grafana_logs_uid", "grafana_logs_type", "grafana_logs_query", "grafana_ca_file", "grafana_proxy",
		"notion_ca_file", "notion_proxy",
		"weather_open_meteo_url", "weather_geocoding_url", "open_meteo_ca_file", "open_meteo_proxy",
		"media_tmdb_url", "media_tmdb_images_url", "media_eztv_url", "tmdb_ca_file", "tmdb_proxy",
		"webpush_subject", "webpush_ca_file",
		"mail_embed_url", "mail_embed_model", "mail_embed_ca_file", "mail_embed_proxy",
		"mail_ship24_url", "ship24_ca_file", "ship24_proxy",
	})
	if err != nil {
		return out, err
	}
	key, err := sourceSecret(ctx, src, pins, "anthropic_api_key", "api_key")
	if err != nil {
		return out, err
	}
	out.Services = append(out.Services, anthropicconn.ImportV2(values, key))
	if key.Value != "" {
		out.Secrets = append(out.Secrets, "anthropic_api_key")
	}
	if values[grafanatools.KeyURL] != "" {
		token, err := sourceSecret(ctx, src, pins, "grafana_viewer_token", "viewer_token")
		if err != nil {
			return out, err
		}
		out.Services = append(out.Services, grafanatools.ImportV2(values, token))
		if token.Value != "" {
			out.Secrets = append(out.Secrets, "grafana_viewer_token")
		}
	}
	out.Services = append(out.Services, weather.ImportV2(values), webfetch.ImportV2())
	tmdbToken, err := sourceSecret(ctx, src, pins, "media_tmdb_token", "read_token")
	if err != nil {
		return out, err
	}
	out.Services = append(out.Services, media.ImportV2(values, tmdbToken)...)
	if tmdbToken.Value != "" {
		out.Secrets = append(out.Secrets, "media_tmdb_token")
	}
	token, err := sourceSecret(ctx, src, pins, "notion_token", "token")
	if err != nil {
		return out, err
	}
	out.Services = append(out.Services, notion.ImportV2(values, token))
	if token.Value != "" {
		out.Secrets = append(out.Secrets, "notion_token")
	}
	vapid, err := sourceSecret(ctx, src, pins, "webpush_vapid_private_key", "vapid_private")
	if err != nil {
		return out, err
	}
	out.Services = append(out.Services, notify.ImportV2(values, vapid))
	if vapid.Value != "" {
		out.Secrets = append(out.Secrets, "webpush_vapid_private_key")
	}
	google, googleSecrets, err := sourceGoogle(ctx, src, pins)
	if err != nil {
		return out, err
	}
	if google.ID != "" {
		out.Services = append(out.Services, google)
		out.Secrets = append(out.Secrets, googleSecrets...)
	}
	iclouds, icloudNotes, err := sourceICloud(ctx, src, pins)
	if err != nil {
		return out, err
	}
	out.Services = append(out.Services, iclouds...)
	out.Notes = append(out.Notes, icloudNotes...)
	mails, mailSecrets, mailNotes, err := sourceMailAccounts(ctx, src, pins)
	if err != nil {
		return out, err
	}
	out.Services = append(out.Services, mails...)
	out.Secrets = append(out.Secrets, mailSecrets...)
	out.Notes = append(out.Notes, mailNotes...)
	for _, service := range mails {
		out.MailIDs = append(out.MailIDs, service.ID)
	}
	oidcSecret, err := sourceSecret(ctx, src, pins, "oidc_client_secret", "client_secret")
	if err != nil {
		return out, err
	}
	out.Notes = append(out.Notes, oidcconn.ImportV2(oidcSecret)...)
	embedKey, err := sourceSecret(ctx, src, pins, "mail_embed_key", "bearer")
	if err != nil {
		return out, err
	}
	probe, err := sourceProbe(ctx, src, "embed")
	if err != nil {
		return out, err
	}
	var embedHealth *connections.ImportHealth
	if state, reason, checked, expires, ok, err := sourceHealth(ctx, src, "mail_embed"); err != nil {
		return out, err
	} else if ok {
		embedHealth = &connections.ImportHealth{State: state, Reason: reason, CheckedAt: checked, ExpiresAt: expires}
	}
	embedding, embedNotes := endpoint.ImportV2(values, probe, embedHealth, accounts["embed"], embedKey)
	out.Services = append(out.Services, embedding)
	out.Notes = append(out.Notes, embedNotes...)
	if embedKey.Value != "" {
		out.Secrets = append(out.Secrets, "mail_embed_key")
	}
	ship24Key, err := sourceSecret(ctx, src, pins, "mail_ship24_api_key", "api_key")
	if err != nil {
		return out, err
	}
	var ship24Health *connections.ImportHealth
	if state, reason, checked, expires, ok, err := sourceHealth(ctx, src, "ship24"); err != nil {
		return out, err
	} else if ok {
		ship24Health = &connections.ImportHealth{State: state, Reason: reason, CheckedAt: checked, ExpiresAt: expires}
	}
	out.Services = append(out.Services, tracking.ImportV2(values, ship24Key, ship24Health))
	if ship24Key.Value != "" {
		out.Secrets = append(out.Secrets, "mail_ship24_api_key")
	}
	return out, nil
}

func sourceProbe(ctx context.Context, src *sql.DB, kind string) (*endpoint.ImportProbe, error) {
	var (
		p         endpoint.ImportProbe
		models    string
		probed    float64
		dimension sql.NullInt64
	)
	err := src.QueryRowContext(ctx, "SELECT fingerprint, ok, reason, dimension, models, probed_at FROM endpoint_probes WHERE kind = ?", kind).
		Scan(&p.Fingerprint, &p.OK, &p.Reason, &dimension, &models, &probed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.ProbedAt = probed
	if dimension.Valid {
		p.Dimension = int(dimension.Int64)
	}
	_ = json.Unmarshal([]byte(models), &p.Models)
	return &p, nil
}

func sourceGoogle(ctx context.Context, src *sql.DB, pins *sourcePins) (connections.ImportService, []string, error) {
	var service connections.ImportService
	values, err := sourceSettings(ctx, src, pins, []string{"google_client_id"})
	if err != nil {
		return service, nil, err
	}
	secret, err := sourceSecret(ctx, src, pins, "google_oauth_client_secret", "oauth/client_secret")
	if err != nil {
		return service, nil, err
	}
	calendarGrant, err := sourceSecret(ctx, src, pins, "google_calendar_refresh_token", "oauth/calendar/refresh")
	if err != nil {
		return service, nil, err
	}
	gmailGrant, err := sourceSecret(ctx, src, pins, "google_gmail_refresh_token", "oauth/gmail/refresh")
	if err != nil {
		return service, nil, err
	}
	if values["google_client_id"] == "" && secret.Value == "" && calendarGrant.Value == "" && gmailGrant.Value == "" {
		return service, nil, nil
	}
	var capabilities []string
	if calendarGrant.Value != "" {
		capabilities = append(capabilities, "calendar")
	}
	if gmailGrant.Value != "" {
		capabilities = append(capabilities, "gmail")
	}
	service = connections.ImportService{
		ID: "google/default", Kind: "google", Label: "Google", Source: connections.SourceOperator,
		Config:       map[string]string{"client_id": values["google_client_id"]},
		Capabilities: capabilities,
		Credentials:  []connections.ImportCredential{secret, calendarGrant, gmailGrant},
	}
	if state, reason, checked, expires, ok, err := sourceHealth(ctx, src, "google"); err != nil {
		return service, nil, err
	} else if ok {
		service.Health = &connections.ImportHealth{State: state, Reason: reason, CheckedAt: checked, ExpiresAt: expires}
	}
	secrets := []string{"google_oauth_client_secret", "google_calendar_refresh_token", "google_gmail_refresh_token"}
	return service, secrets, nil
}

func sourceHealth(ctx context.Context, src *sql.DB, name string) (string, string, float64, *float64, bool, error) {
	var state, reason string
	var checked float64
	var expires sql.NullFloat64
	err := src.QueryRowContext(ctx, "SELECT state, reason, checked_at, expires_at FROM integration_health WHERE name = ?", name).
		Scan(&state, &reason, &checked, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, nil, false, nil
	}
	if err != nil {
		return "", "", 0, nil, false, err
	}
	var expiry *float64
	if expires.Valid {
		expiry = &expires.Float64
	}
	return state, reason, checked, expiry, true, nil
}

type icloudAccount struct{ id, login string }

func sourceICloudAccounts(ctx context.Context, src *sql.DB) ([]icloudAccount, error) {
	rows, err := src.QueryContext(ctx, "SELECT id, login FROM mail_accounts WHERE preset = 'icloud' AND enabled = 1 ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var accounts []icloudAccount
	for rows.Next() {
		var a icloudAccount
		if err := rows.Scan(&a.id, &a.login); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

func sourceICloudSelection(ctx context.Context, src *sql.DB, pins *sourcePins) (string, []icloudAccount, error) {
	values, err := sourceSettings(ctx, src, pins, []string{"icloud_apple_id", "calendar_account"})
	if err != nil {
		return "", nil, err
	}
	enabled, err := sourceICloudAccounts(ctx, src)
	if err != nil {
		return "", nil, err
	}
	selected := strings.TrimSpace(values["calendar_account"])
	accounts := enabled
	if selected != "" {
		accounts = nil
		for _, a := range enabled {
			if a.id == selected {
				accounts = append(accounts, a)
			}
		}
	}
	appleID := values["icloud_apple_id"]
	if appleID != "" && len(accounts) != 1 {
		return "", nil, icloudSelectionError(enabled, selected)
	}
	return appleID, accounts, nil
}

func icloudSelectionError(enabled []icloudAccount, selected string) error {
	ids := make([]string, 0, len(enabled))
	for _, a := range enabled {
		ids = append(ids, a.id)
	}
	choice := "enable exactly one iCloud account"
	if len(ids) > 0 {
		choice = "set calendar_account to one of: " + strings.Join(ids, ", ")
	}
	if selected != "" {
		return fmt.Errorf("db import-v2: calendar.icloud_apple_id is set but calendar_account %q does not select exactly one enabled iCloud account; %s", selected, choice)
	}
	return fmt.Errorf("db import-v2: calendar.icloud_apple_id is set but cannot be placed on exactly one iCloud account; %s", choice)
}

func sourceICloud(ctx context.Context, src *sql.DB, pins *sourcePins) ([]connections.ImportService, []string, error) {
	appleID, accounts, err := sourceICloudSelection(ctx, src, pins)
	if err != nil {
		return nil, nil, err
	}
	if len(accounts) > 1 {
		return nil, []string{"icloud: unresolved-selection; set calendar_account to one of the enabled iCloud accounts"}, nil
	}
	var services []connections.ImportService
	for _, a := range accounts {
		service := connections.ImportService{
			ID: "icloud/" + a.id, Kind: string(calendar.KindICloud), Label: "iCloud " + a.id, Source: connections.SourceOperator,
			Config:       map[string]string{"apple_id": appleID},
			Capabilities: []string{calendar.CapabilityCalDAV, calendar.CapabilityCardDAV},
		}
		services = append(services, service)
	}
	return services, nil, nil
}

func sourceMailAccounts(ctx context.Context, src *sql.DB, pins *sourcePins) ([]connections.ImportService, []string, []string, error) {
	rows, err := src.QueryContext(ctx, "SELECT id, label, preset, login, enabled, auth_method FROM mail_accounts ORDER BY id")
	if err != nil {
		return nil, nil, nil, err
	}
	type account struct {
		id, label, preset, login, method string
		enabled                          bool
	}
	var accounts []account
	for rows.Next() {
		var a account
		if err := rows.Scan(&a.id, &a.label, &a.preset, &a.login, &a.enabled, &a.method); err != nil {
			_ = rows.Close()
			return nil, nil, nil, err
		}
		accounts = append(accounts, a)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, nil, err
	}
	var (
		services []connections.ImportService
		secrets  []string
		notes    []string
	)
	for _, a := range accounts {
		if a.id == "chat" || a.id == "embed" {
			notes = append(notes, "mail/"+a.id+": ambiguous-health; the account id collides with a local endpoint name")
		}
		enabled := a.enabled
		service := connections.ImportService{
			ID: "mail/" + a.id, Kind: "mail", Label: a.label, Source: connections.SourceOperator,
			Config: map[string]string{
				"preset": a.preset, "login": a.login, "auth_method": a.method,
				"enabled": fmt.Sprintf("%t", a.enabled),
			},
			Capabilities: []string{"imap", "smtp"},
			Enabled:      &enabled,
		}
		password, err := sourceSecret(ctx, src, pins, "mail_password_"+a.id, "mail/"+a.id+"/password")
		if err != nil {
			return nil, nil, nil, err
		}
		if password.Value != "" {
			service.Credentials = []connections.ImportCredential{password}
			secrets = append(secrets, "mail_password_"+a.id)
		} else if a.method == "password" {
			notes = append(notes, "mail/"+a.id+": unresolved-credential; the app password is not in the source database")
		}
		if a.id != "chat" && a.id != "embed" {
			state, reason, checked, expires, ok, err := sourceHealth(ctx, src, "mail_"+a.id)
			if err != nil {
				return nil, nil, nil, err
			}
			if ok {
				service.Health = &connections.ImportHealth{State: state, Reason: reason, CheckedAt: checked, ExpiresAt: expires}
			}
		}
		services = append(services, service)
	}
	return services, secrets, notes, nil
}

func sourceSettings(ctx context.Context, src *sql.DB, pins *sourcePins, keys []string) (map[string]string, error) {
	args := make([]any, len(keys))
	for i, key := range keys {
		args[i] = key
	}
	rows, err := src.QueryContext(ctx, "SELECT key, value FROM settings WHERE key IN ("+placeholders(len(keys))+")", args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, key := range keys {
		if pin, ok := pins.m[key]; ok && !settings.ImportSecretName(key) {
			values[key] = pin.Value
		}
	}
	return values, nil
}

func sourceSecret(ctx context.Context, src *sql.DB, pins *sourcePins, name, slot string) (connections.ImportCredential, error) {
	c := connections.ImportCredential{Slot: slot}
	if _, ok := pins.m[name]; ok {
		return c, nil
	}
	err := src.QueryRowContext(ctx, "SELECT value, set_at, set_by FROM secrets WHERE name = ?", name).
		Scan(&c.Value, &c.SetAt, &c.SetBy)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	return c, nil
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
		switch {
		case (plan.action == fresh || plan.action == derive) && present[plan.table]:
			return nil, fmt.Errorf("db import-v2: the source has a %s table; it is not a v2 database at version %d",
				plan.table, v2Version)
		case plan.action != fresh && plan.action != derive && !present[plan.table]:
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
	if plan.action == fresh || plan.action == derive {
		return im.countRows(plan.table)
	}
	if plan.run != nil {
		return plan.run(im)
	}
	if plan.action == drop {
		return im.countSource(plan.table)
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

func (im *importer) carryAudit() (int64, error) {
	var sourceMax, sourceCount int64
	if err := im.src.QueryRowContext(im.ctx, "SELECT COALESCE(MAX(id), 0), COUNT(*) FROM settings_audit").Scan(&sourceMax, &sourceCount); err != nil {
		return 0, err
	}
	if sourceCount == 0 {
		return 0, nil
	}
	var destMax int64
	if err := im.tx.QueryRowContext(im.ctx, "SELECT COALESCE(MAX(id), 0) FROM settings_audit").Scan(&destMax); err != nil {
		return 0, err
	}
	if destMax > 0 {
		if err := im.renumberAudit(sourceMax, destMax); err != nil {
			return 0, err
		}
	}
	rows, err := im.src.QueryContext(im.ctx,
		"SELECT id, ts, key, old_value, new_value, secret, actor, source FROM settings_audit ORDER BY id")
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var n int64
	for rows.Next() {
		var (
			id     int64
			ts     float64
			key    string
			old    sql.NullString
			next   sql.NullString
			secret int
			actor  string
			source string
		)
		if err := rows.Scan(&id, &ts, &key, &old, &next, &secret, &actor, &source); err != nil {
			return n, err
		}
		if _, err := im.tx.ExecContext(im.ctx,
			"INSERT INTO settings_audit (id, ts, key, old_value, new_value, secret, actor, source) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			id, ts, key, old, next, secret, actor, source); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func (im *importer) renumberAudit(sourceMax, destMax int64) error {
	offset := sourceMax + destMax
	for _, statement := range []string{
		"DROP TRIGGER IF EXISTS settings_audit_no_update",
		"DROP TRIGGER IF EXISTS settings_audit_no_delete",
	} {
		if _, err := im.tx.ExecContext(im.ctx, statement); err != nil {
			return err
		}
	}
	if _, err := im.tx.ExecContext(im.ctx, "UPDATE settings_audit SET id = id + ?", offset); err != nil {
		return err
	}
	if _, err := im.tx.ExecContext(im.ctx, "INSERT INTO sqlite_sequence (name, seq) SELECT 'settings_audit', ? WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'settings_audit')", destMax+offset); err != nil {
		return err
	}
	if _, err := im.tx.ExecContext(im.ctx, "UPDATE sqlite_sequence SET seq = ? WHERE name = 'settings_audit'", destMax+offset); err != nil {
		return err
	}
	for _, statement := range []string{
		"CREATE TRIGGER IF NOT EXISTS settings_audit_no_update BEFORE UPDATE ON settings_audit BEGIN SELECT RAISE(ABORT, 'settings_audit is append-only'); END",
		"CREATE TRIGGER IF NOT EXISTS settings_audit_no_delete BEFORE DELETE ON settings_audit BEGIN SELECT RAISE(ABORT, 'settings_audit is append-only'); END",
	} {
		if _, err := im.tx.ExecContext(im.ctx, statement); err != nil {
			return err
		}
	}
	return nil
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
		e.Month = inference.MonthKey(im.now, im.loc)
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
		if (plan.action != carry && plan.action != fresh && plan.table != "jobs") || plan.table == "mail_accounts" || plan.table == "subagents" {
			continue
		}
		query := "SELECT COUNT(*) FROM " + quoteName(plan.table)
		if plan.table == "operation_audit" {
			query += " WHERE operation NOT LIKE 'maintenance.%'"
		}
		var n int64
		if err := q.QueryRowContext(ctx, query).Scan(&n); err != nil {
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
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM inference_classification_deferrals").Scan(&n); err != nil {
		return fmt.Errorf("db import-v2: destination: %w", err)
	}
	if n > 0 {
		counts = append(counts, fmt.Sprintf("inference_classification_deferrals=%d", n))
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

func jobDefinitions(ctx context.Context) []scheduler.Definition {
	set := module.SetFrom(ctx)
	if set == nil {
		return nil
	}
	return set.Catalog().Jobs()
}

func (im *importer) jobs() (int64, error) {
	if module.SetFrom(im.ctx) == nil {
		return 0, errors.New("no module catalog is bound; declared jobs cannot be matched")
	}
	n, notes, err := scheduler.ImportJobs(im.ctx, im.src, im.tx, jobDefinitions(im.ctx), im.now)
	im.notes = append(im.notes, notes...)
	return n, err
}
