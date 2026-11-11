package writes

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

type Write struct {
	ID         int64
	TS         float64
	ThreadID   *int64
	RunID      *int64
	Tool       string
	Target     string
	Before     *string
	After      *string
	Undo       *string
	UndoneAt   *float64
	State      string
	Error      *string
	ResolvedAt *float64
}

const writeColumns = "id, ts, thread_id, run_id, tool, target, before_json, after_json, undo_json, undone_at, state, error, resolved_at"

func scanWrite(row interface{ Scan(...any) error }) (Write, error) {
	var w Write
	err := row.Scan(&w.ID, &w.TS, &w.ThreadID, &w.RunID, &w.Tool, &w.Target, &w.Before, &w.After, &w.Undo, &w.UndoneAt, &w.State, &w.Error, &w.ResolvedAt)
	return w, err
}

func FromClause(alias string) string { return "agent_writes " + alias }

func ByID(ctx context.Context, q db.Querier, id int64) (*Write, error) {
	w, err := scanWrite(q.QueryRowContext(ctx, "SELECT "+writeColumns+" FROM agent_writes WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func List(ctx context.Context, q db.Querier, where string, args []any, limit int) ([]Write, error) {
	query := "SELECT " + writeColumns + " FROM agent_writes"
	if where != "" {
		query += " WHERE " + where
	}
	query += " ORDER BY ts DESC, id DESC LIMIT ?"
	rows, err := q.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Write, error) { return scanWrite(r) })
}

func TargetsForRuns(ctx context.Context, q db.Querier, runIDs []int64, tool string) ([]string, error) {
	if len(runIDs) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(runIDs)), ",")
	args := make([]any, len(runIDs))
	for i, id := range runIDs {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, "SELECT target FROM agent_writes WHERE run_id IN ("+marks+") AND tool = ?", append(args, tool)...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var target string
		err := r.Scan(&target)
		return target, err
	})
}

func CountForRuns(ctx context.Context, q db.Querier, runIDs []int64, tool string) (int, error) {
	if len(runIDs) == 0 {
		return 0, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(runIDs)), ",")
	args := make([]any, len(runIDs))
	for i, id := range runIDs {
		args[i] = id
	}
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_writes WHERE run_id IN ("+marks+") AND tool = ?", append(args, tool)...).Scan(&n)
	return n, err
}

func UnknownExists(ctx context.Context, q db.Querier, runID int64, tool string, since float64) (bool, error) {
	var found bool
	err := q.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM agent_writes WHERE run_id = ? AND tool = ? AND state = 'unknown' AND ts >= ?)",
		runID, tool, since).Scan(&found)
	return found, err
}

func ThreadIDsForTargets(ctx context.Context, q db.Querier, targets []string) ([]int64, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(targets)), ",")
	args := make([]any, len(targets))
	for i, target := range targets {
		args[i] = target
	}
	rows, err := q.QueryContext(ctx,
		"SELECT DISTINCT thread_id FROM agent_writes WHERE target IN ("+marks+") AND state = 'ok' AND undone_at IS NULL AND thread_id IS NOT NULL", args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		err := r.Scan(&id)
		return id, err
	})
}

func PurgeTarget(ctx context.Context, q db.Querier, target string) error {
	_, err := q.ExecContext(ctx,
		"UPDATE agent_writes SET before_json = '{\"purged\":true}', after_json = '{\"purged\":true}', undo_json = '{\"purged\":true}' WHERE target = ?", target)
	return err
}

func BlockedCount(ctx context.Context, q db.Querier, tool, target string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM agent_writes WHERE tool = ? AND target = ? AND state IN ('held', 'unknown')", tool, target).Scan(&n)
	return n, err
}

func CountSince(ctx context.Context, q db.Querier, tool string, since float64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_writes WHERE tool = ? AND ts >= ?", tool, since).Scan(&n)
	return n, err
}

func TargetsLike(ctx context.Context, q db.Querier, like, excludeTool string) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT target FROM agent_writes WHERE target LIKE ? AND tool != ?", like, excludeTool)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var target string
		err := r.Scan(&target)
		return target, err
	})
}

type HistoryRow struct {
	ID       int64
	TS       float64
	Tool     string
	UndoneAt *float64
}

func History(ctx context.Context, q db.Querier, target string) ([]HistoryRow, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, ts, tool, undone_at FROM agent_writes WHERE target = ? ORDER BY id DESC", target)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (HistoryRow, error) {
		var h HistoryRow
		err := r.Scan(&h.ID, &h.TS, &h.Tool, &h.UndoneAt)
		return h, err
	})
}

type Resolution struct {
	ID         int64
	TS         float64
	Actor      string
	Kind       string
	RowID      int64
	ProposalID *int64
	Outcome    string
}

func ResolutionsSince(ctx context.Context, q db.Querier, limit int) ([]Resolution, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, ts, actor, kind, row_id, proposal_id, outcome FROM write_resolutions WHERE source = 'phone' ORDER BY ts DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Resolution, error) {
		var x Resolution
		err := r.Scan(&x.ID, &x.TS, &x.Actor, &x.Kind, &x.RowID, &x.ProposalID, &x.Outcome)
		return x, err
	})
}

type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func RecordResolution(ctx context.Context, x Execer, actor, source string, ts float64, kind string, rowID int64, proposalID *int64, outcome, prior string) error {
	var proposal any
	if proposalID != nil {
		proposal = *proposalID
	}
	_, err := x.ExecContext(ctx,
		"INSERT INTO write_resolutions (ts, actor, source, kind, row_id, proposal_id, outcome, prior_error) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		ts, actor, source, kind, rowID, proposal, outcome, prior)
	return err
}
