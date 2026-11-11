package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/db"
)

type Estimate struct {
	Threads int
	Nodes   int
	CostUSD float64
}

// EstimateBackfill counts the threads and pending nodes a full backfill would
// summarize and prices them with the current memory model.
func EstimateBackfill(ctx context.Context, q db.Querier) (Estimate, error) {
	var out Estimate
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM threads t WHERE t.deleted_at IS NULL AND EXISTS (SELECT 1 FROM thread_messages m WHERE m.thread_id = t.id AND m.superseded = 0)").
		Scan(&out.Threads); err != nil {
		return out, err
	}
	var messages int64
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM thread_messages m JOIN threads t ON t.id = m.thread_id WHERE m.superseded = 0 AND t.deleted_at IS NULL").
		Scan(&messages); err != nil {
		return out, err
	}
	out.Nodes = int(messages * 2)
	nodeBytes := TuningMemoryNodeBytes
	model, err := Model(ctx, q)
	if err != nil {
		return out, err
	}
	perNode, err := pricing.CostUSD(model, pricing.Tokens{Input: int64(nodeBytes / 4), Output: int64(nodeBytes / 4)}, 0)
	if err != nil {
		return out, err
	}
	out.CostUSD = float64(out.Nodes) * perNode
	return out, nil
}

// Backfill builds and drains the memory tree for one thread or every thread.
// It is idempotent: existing nodes are kept and only pending nodes are queued.
func Backfill(ctx context.Context, d *db.DB, threadID *int64, client *anthropic.Client, model, effort string, now float64) (int, error) {
	threads, err := backfillThreads(ctx, d.Read, threadID)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, id := range threads {
		if err := Rebuild(ctx, d.Write, id, now); err != nil {
			return done, err
		}
		for range 100 {
			found, err := QueuedThreads(ctx, d.Read, now)
			if err != nil {
				return done, err
			}
			queued := false
			for _, qid := range found {
				if qid == id {
					queued = true
					break
				}
			}
			if !queued {
				break
			}
			n, err := Work(ctx, d, id, WorkOptions{Now: now, Client: client, Model: model, Effort: effort})
			if err != nil {
				return done, err
			}
			if n == 0 {
				break
			}
			done += n
		}
	}
	if threadID == nil {
		err := d.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				"INSERT INTO memory_state (key, value) VALUES ('backfill_done', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
				fmt.Sprintf("%d", time.Now().Unix()))
			return err
		})
		if err != nil {
			return done, err
		}
	}
	return done, nil
}

func backfillThreads(ctx context.Context, q db.Querier, threadID *int64) ([]int64, error) {
	if threadID != nil {
		return []int64{*threadID}, nil
	}
	rows, err := q.QueryContext(ctx,
		"SELECT t.id FROM threads t WHERE t.deleted_at IS NULL AND EXISTS (SELECT 1 FROM thread_messages m WHERE m.thread_id = t.id AND m.superseded = 0) ORDER BY t.id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
}

// BackfillDone reports the recorded completion timestamp, if any.
func BackfillDone(ctx context.Context, q db.Querier) (string, error) {
	var value string
	err := q.QueryRowContext(ctx, "SELECT value FROM memory_state WHERE key = 'backfill_done'").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}
