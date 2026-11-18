package kb

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	ScopeOps     = "ops"
	WriterWorker = "worker"
	opsOrigin    = "ops:"
)

type OpsPage struct {
	Fact     string
	Title    string
	Body     string
	Marks    tool.Marks
	ThreadID int64
	RunID    int64
	Expires  float64
	Now      float64
}

func OpsOrigin(fact string) string { return opsOrigin + fact }

func OpsEffect(operation string, threadID, runID int64) ledger.New {
	return ledger.New{Origin: "agent", Operation: operation, OperationKey: operation + ":" + ledger.NewKey(),
		Target: "kb:new", ThreadID: nullable(threadID), RunID: nullable(runID)}
}

func nullable(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

func WriteOpsPageTx(ctx context.Context, tx *sql.Tx, p OpsPage) (Record, ledger.Result, error) {
	origin := OpsOrigin(p.Fact)
	aliases, err := aliasesJSON([]string{p.Fact})
	if err != nil {
		return Record{}, ledger.Result{}, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM kb_records WHERE origin = ?", origin).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx,
			"INSERT INTO kb_records (title, aliases, body, scope, private, marks, writer, source_thread_id, source_run_id, origin, "+
				"pinned, expires_at, created_at, updated_at) VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, 0, ?, ?, ?)",
			p.Title, aliases, p.Body, ScopeOps, int64(p.Marks), WriterWorker, nullable(p.ThreadID), nullable(p.RunID), origin,
			p.Expires, p.Now, p.Now)
		if err != nil {
			return Record{}, ledger.Result{}, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return Record{}, ledger.Result{}, err
		}
		rec, err := Get(ctx, tx, id)
		if err != nil {
			return Record{}, ledger.Result{}, err
		}
		return *rec, writeResult(*rec, nil), nil
	case err != nil:
		return Record{}, ledger.Result{}, err
	}
	rec, err := Get(ctx, tx, id)
	if err != nil {
		return Record{}, ledger.Result{}, err
	}
	if err := refused(*rec, "updated"); err != nil {
		return Record{}, ledger.Result{}, err
	}
	next := *rec
	next.Title, next.Body, next.Aliases = p.Title, p.Body, []string{p.Fact}
	next.Marks |= p.Marks
	next.ExpiresAt, next.UpdatedAt = &p.Expires, p.Now
	if _, err := tx.ExecContext(ctx,
		"UPDATE kb_records SET title = ?, aliases = ?, body = ?, marks = ?, source_thread_id = ?, source_run_id = ?, expires_at = ?, updated_at = ? WHERE id = ?",
		next.Title, aliases, next.Body, int64(next.Marks), nullable(p.ThreadID), nullable(p.RunID), p.Expires, p.Now, id); err != nil {
		return Record{}, ledger.Result{}, err
	}
	return next, writeResult(next, beforeOf(*rec)), nil
}

func LiveOpsFacts(ctx context.Context, q db.Querier, now float64) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT substr(origin, ?) FROM kb_records WHERE scope = ? AND origin LIKE ? AND retracted_at IS NULL AND (expires_at IS NULL OR expires_at > ?) ORDER BY id",
		len(opsOrigin)+1, ScopeOps, opsOrigin+"%", now)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var fact string
		err := r.Scan(&fact)
		return fact, err
	})
}

func LiveOpsPages(ctx context.Context, q db.Querier, now float64, limit int) ([]Record, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT "+columns+" FROM kb_records r WHERE r.scope = ? AND r.origin LIKE ? AND r.retracted_at IS NULL AND (r.expires_at IS NULL OR r.expires_at > ?) ORDER BY r.updated_at DESC, r.id DESC LIMIT ?",
		ScopeOps, opsOrigin+"%", now, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Record, error) { return scan(r) })
}

func ExpireOpsPage(ctx context.Context, q db.Querier, fact string, now float64) error {
	_, err := q.ExecContext(ctx,
		"UPDATE kb_records SET expires_at = ?, updated_at = ? WHERE origin = ? AND scope = ? AND (expires_at IS NULL OR expires_at > ?)",
		now, now, OpsOrigin(fact), ScopeOps, now)
	return err
}

func OpsPageLocked(ctx context.Context, q db.Querier, fact string) (bool, error) {
	found, err := scan(q.QueryRowContext(ctx, "SELECT "+columns+" FROM kb_records r WHERE r.origin = ?", OpsOrigin(fact)))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return refused(found, "updated") != nil, nil
}
