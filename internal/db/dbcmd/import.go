package dbcmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/rules"
)

var importedTables = []string{"accounts", "mailboxes", "messages", "corrections", "decisions", "rules"}

var messageColumns = []string{
	"id", "account", "mailbox_id", "uidvalidity", "uid", "msgid", "digest", "gm_msgid", "gm_thrid", "internaldate",
	"date_ts", "size", "raw_path", "flags", "seen", "flagged", "labels", "in_inbox", "sent", "location", "folder",
	"category", "from_addr", "from_domain", "from_name", "to_addrs", "reply_to", "rpath_domain", "list_id",
	"precedence", "subject", "subject_tmpl", "msgid_domain", "in_reply_to", "refs", "dkim_d", "esp", "att_types",
	"att_names", "k_order", "k_vcode", "k_otp6", "embedded", "first_seen", "gone_at",
}

type importReport struct {
	Rules       int64
	Corrections int64
	Decisions   int64
	Messages    int64
	Accounts    int64
	Mailboxes   int64
}

func (s *service) importV1(a *cli.Args) cli.Handler {
	src := a.Arg("path", cli.Opt{Help: "path to the v1 database to copy the learned-rule data from"})
	return func(e *cli.Env) int {
		cfg, err := config.Load(config.OS)
		if err != nil {
			return cli.Fail(e, err)
		}
		key, err := s.currentKey()
		if err != nil {
			return cli.Fail(e, err)
		}
		if err := importV1(e.Ctx, cfg, *src, key); err != nil {
			return cli.Fail(e, err)
		}
		report, err := importCounts(e.Ctx, cfg.DBPath, key)
		if err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "imported rules: %d\nimported corrections: %d\nimported decisions: %d\n"+
			"imported messages: %d\nimported accounts: %d\nimported mailboxes: %d\n",
			report.Rules, report.Corrections, report.Decisions, report.Messages, report.Accounts, report.Mailboxes)
		return cli.ExitOK
	}
}

func importV1(ctx context.Context, cfg config.Config, source string, key db.Key) error {
	if cfg.DBPath == source {
		return errors.New("db import-v1: the source and the target database are the same file")
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := requireV2(ctx, d.Read); err != nil {
		return err
	}
	if err := config.Migrate(ctx, d, cfg); err != nil {
		return err
	}
	if err := refusePopulated(ctx, d.Read); err != nil {
		return err
	}
	src, err := db.OpenSource(source, key)
	if err != nil {
		return fmt.Errorf("db import-v1: %w", err)
	}
	defer func() { _ = src.Close() }()
	if err := requireColumns(ctx, src, "messages", messageColumns); err != nil {
		return err
	}
	if err := validateRules(ctx, src); err != nil {
		return err
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := copyAccounts(ctx, src, tx); err != nil {
			return err
		}
		if err := copyMailboxes(ctx, src, tx); err != nil {
			return err
		}
		if err := copyMessages(ctx, src, tx); err != nil {
			return err
		}
		if err := copyCorrections(ctx, src, tx); err != nil {
			return err
		}
		if err := copyDecisions(ctx, src, tx); err != nil {
			return err
		}
		return copyRules(ctx, src, tx)
	})
}

func requireV2(ctx context.Context, q db.Querier) error {
	var n int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		var tables int
		if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return err
		}
		if tables > 0 {
			return errors.New("db import-v1: the target database is not a v2 database; import into a fresh path")
		}
		return nil
	}
	return db.RefuseFutureSchema(ctx, q)
}

func refusePopulated(ctx context.Context, q db.Querier) error {
	var counts []string
	for _, table := range importedTables {
		var n int64
		if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			counts = append(counts, fmt.Sprintf("%s=%d", table, n))
		}
	}
	if len(counts) > 0 {
		return fmt.Errorf("db import-v1: the target database already holds imported data (%s); import into a fresh database",
			strings.Join(counts, ", "))
	}
	return nil
}

func requireColumns(ctx context.Context, q db.Querier, table string, columns []string) error {
	rows, err := q.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return err
	}
	have, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var name string
		err := r.Scan(&name)
		return name, err
	})
	if err != nil {
		return err
	}
	var missing []string
	for _, c := range columns {
		if !slices.Contains(have, c) {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("db import-v1: the source %s table lacks %s; it is not a v1 database this build can import",
			table, strings.Join(missing, ", "))
	}
	return nil
}

type storedCondition struct {
	Field string          `json:"field"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value"`
}

type ruleProblem struct {
	ID      string
	Field   string
	Pattern string
	Err     string
}

func validateRules(ctx context.Context, q db.Querier) error {
	rows, err := q.QueryContext(ctx, "SELECT id, account, action, dest, conditions FROM rules ORDER BY position")
	if err != nil {
		return err
	}
	var problems []ruleProblem
	err = func() error {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id, action, text string
			var account, dest sql.NullString
			if err := rows.Scan(&id, &account, &action, &dest, &text); err != nil {
				return err
			}
			row := rules.StoredRule{ID: id, Action: action, Conditions: text}
			if account.Valid {
				row.Account = &account.String
			}
			if dest.Valid {
				row.Dest = &dest.String
			}
			if err := rules.ValidateStored(row); err != nil {
				field, pattern := offendingCondition(text)
				problems = append(problems, ruleProblem{ID: id, Field: field, Pattern: pattern, Err: err.Error()})
			}
		}
		return rows.Err()
	}()
	if err != nil {
		return err
	}
	if len(problems) == 0 {
		return nil
	}
	lines := make([]string, len(problems))
	for i, p := range problems {
		lines[i] = fmt.Sprintf("rule %s: field %s: pattern %s: %s", p.ID, p.Field, p.Pattern, p.Err)
	}
	return fmt.Errorf("db import-v1: %d stored rule(s) are invalid; nothing was imported:\n  %s",
		len(problems), strings.Join(lines, "\n  "))
}

func offendingCondition(text string) (field, pattern string) {
	var conditions []storedCondition
	if err := json.Unmarshal([]byte(text), &conditions); err != nil {
		return "", ""
	}
	for _, c := range conditions {
		if c.Op == "re" {
			var p string
			_ = json.Unmarshal(c.Value, &p)
			return c.Field, p
		}
	}
	if len(conditions) > 0 {
		return conditions[0].Field, ""
	}
	return "", ""
}

func copyAccounts(ctx context.Context, src *sql.DB, tx *sql.Tx) error {
	rows, err := src.QueryContext(ctx, "SELECT name, kind, last_sync_at FROM accounts WHERE name IN (SELECT DISTINCT account FROM messages WHERE sent = 0)")
	if err != nil {
		return err
	}
	items, err := db.Collect(rows, func(r *sql.Rows) ([]any, error) {
		var name, kind string
		var last sql.NullFloat64
		if err := r.Scan(&name, &kind, &last); err != nil {
			return nil, err
		}
		return []any{name, kind, last}, nil
	})
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, "INSERT INTO accounts (name, kind, last_sync_at) VALUES (?, ?, ?)", item...); err != nil {
			return err
		}
	}
	return nil
}

func copyMailboxes(ctx context.Context, src *sql.DB, tx *sql.Tx) error {
	rows, err := src.QueryContext(ctx, `SELECT id, account, name, imap_name, role, uidvalidity, highest_uid, highestmodseq, synced_at
		FROM mailboxes WHERE id IN (SELECT DISTINCT mailbox_id FROM messages WHERE sent = 0)`)
	if err != nil {
		return err
	}
	items, err := db.Collect(rows, func(r *sql.Rows) ([]any, error) {
		var id, highest int64
		var account, name, imapName, role string
		var validity, modseq sql.NullInt64
		var synced sql.NullFloat64
		if err := r.Scan(&id, &account, &name, &imapName, &role, &validity, &highest, &modseq, &synced); err != nil {
			return nil, err
		}
		return []any{id, account, name, imapName, role, validity, highest, modseq, synced}, nil
	})
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mailboxes
			(id, account, name, imap_name, role, uidvalidity, highest_uid, highestmodseq, synced_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, item...); err != nil {
			return err
		}
	}
	return nil
}

func copyMessages(ctx context.Context, src *sql.DB, tx *sql.Tx) error {
	rows, err := src.QueryContext(ctx, "SELECT "+strings.Join(messageColumns, ", ")+" FROM messages WHERE sent = 0 ORDER BY id")
	if err != nil {
		return err
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(messageColumns)), ", ")
	insert := "INSERT INTO messages (" + strings.Join(messageColumns, ", ") + ") VALUES (" + marks + ")"
	items, err := db.Collect(rows, func(r *sql.Rows) ([]any, error) {
		values := make([]any, len(messageColumns))
		dest := make([]any, len(messageColumns))
		for i := range values {
			dest[i] = &values[i]
		}
		return values, r.Scan(dest...)
	})
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, insert, item...); err != nil {
			return err
		}
	}
	return nil
}

func copyCorrections(ctx context.Context, src *sql.DB, tx *sql.Tx) error {
	rows, err := src.QueryContext(ctx, `SELECT message_id, account, folder, ts, source FROM corrections
		WHERE message_id IN (SELECT id FROM messages WHERE sent = 0)`)
	if err != nil {
		return err
	}
	items, err := db.Collect(rows, func(r *sql.Rows) ([]any, error) {
		var id int64
		var account, folder, source string
		var ts float64
		if err := r.Scan(&id, &account, &folder, &ts, &source); err != nil {
			return nil, err
		}
		return []any{id, account, folder, ts, source}, nil
	})
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, ?, ?, ?, ?)", item...); err != nil {
			return err
		}
	}
	return nil
}

func copyDecisions(ctx context.Context, src *sql.DB, tx *sql.Tx) error {
	rows, err := src.QueryContext(ctx, `SELECT id, message_id, account, ts, stage, key, action, dest, confidence, detail FROM decisions
		WHERE message_id IN (SELECT id FROM messages WHERE sent = 0) ORDER BY id`)
	if err != nil {
		return err
	}
	items, err := db.Collect(rows, func(r *sql.Rows) ([]any, error) {
		var id, messageID int64
		var account, stage, action, detail string
		var key, dest sql.NullString
		var confidence sql.NullFloat64
		var ts float64
		if err := r.Scan(&id, &messageID, &account, &ts, &stage, &key, &action, &dest, &confidence, &detail); err != nil {
			return nil, err
		}
		return []any{id, messageID, account, ts, stage, key, action, dest, confidence, detail}, nil
	})
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO decisions
			(id, message_id, account, ts, stage, key, action, dest, confidence, detail)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item...); err != nil {
			return err
		}
	}
	return nil
}

func copyRules(ctx context.Context, src *sql.DB, tx *sql.Tx) error {
	rows, err := src.QueryContext(ctx, "SELECT position, id, account, action, dest, conditions FROM rules ORDER BY position")
	if err != nil {
		return err
	}
	items, err := db.Collect(rows, func(r *sql.Rows) ([]any, error) {
		var position int64
		var id, action, conditions string
		var account, dest sql.NullString
		if err := r.Scan(&position, &id, &account, &action, &dest, &conditions); err != nil {
			return nil, err
		}
		return []any{position, id, account, action, dest, conditions}, nil
	})
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, "INSERT INTO rules (position, id, account, action, dest, conditions) VALUES (?, ?, ?, ?, ?, ?)", item...); err != nil {
			return err
		}
	}
	return nil
}

func importCounts(ctx context.Context, path string, key db.Key) (importReport, error) {
	d, err := db.Connect(path, db.WithKey(key))
	if err != nil {
		return importReport{}, err
	}
	defer func() { _ = d.Close() }()
	var r importReport
	for _, item := range []struct {
		table string
		out   *int64
	}{
		{"rules", &r.Rules}, {"corrections", &r.Corrections}, {"decisions", &r.Decisions},
		{"messages", &r.Messages}, {"accounts", &r.Accounts}, {"mailboxes", &r.Mailboxes},
	} {
		if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+item.table).Scan(item.out); err != nil {
			return importReport{}, err
		}
	}
	return r, nil
}
