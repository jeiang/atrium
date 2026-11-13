package progress

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
)

const (
	Complete = "complete"
	RunError = "run_error"
)

type Payload struct {
	Kind string
	Data any
	Time float64
}

type Terminal struct {
	State string
	Time  float64
	Error *string
}

type Batch struct {
	Events        []Event
	Next          int64
	RetainedAfter int64
	TerminalSeq   *int64
}

func Append(ctx context.Context, tx db.Querier, runID int64, p Payload) error {
	rows, err := tx.QueryContext(ctx, "WITH RECURSIVE ancestors(id,parent_run_id) AS (SELECT id,parent_run_id FROM runs WHERE id=? UNION SELECT r.id,r.parent_run_id FROM runs r JOIN ancestors a ON r.id=a.parent_run_id) SELECT id FROM ancestors", runID)
	if err != nil {
		return err
	}
	ids, err := db.Collect(rows, func(r *sql.Rows) (int64, error) { var id int64; err := r.Scan(&id); return id, err })
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := appendOne(ctx, tx, id, runID, p); err != nil {
			return err
		}
	}
	return nil
}

func appendOne(ctx context.Context, tx db.Querier, streamID, runID int64, p Payload) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_streams(run_id) VALUES(?) ON CONFLICT DO NOTHING", streamID); err != nil {
		return err
	}
	var seq int64
	err := tx.QueryRowContext(ctx, "UPDATE run_streams SET next_seq=next_seq+1 WHERE run_id=? AND terminal_seq IS NULL RETURNING next_seq-1", streamID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO run_events(stream_run_id,seq,run_id,ts,kind,data_json) VALUES(?,?,?,?,?,?)", streamID, seq, runID, p.Time, p.Kind, jsonx.Dumps(p.Data, dataOpts))
	return err
}

func Finish(ctx context.Context, tx db.Querier, runID int64, terminal Terminal) error {
	if terminal.State != "done" && terminal.State != "failed" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_streams(run_id) VALUES(?) ON CONFLICT DO NOTHING", runID); err != nil {
		return err
	}
	var finished *int64
	if err := tx.QueryRowContext(ctx, "SELECT terminal_seq FROM run_streams WHERE run_id=?", runID).Scan(&finished); err != nil {
		return err
	}
	if finished != nil {
		return nil
	}
	var threadID int64
	if err := tx.QueryRowContext(ctx, "SELECT thread_id FROM runs WHERE id=?", runID).Scan(&threadID); err != nil {
		return err
	}
	if err := Append(ctx, tx, runID, Payload{Kind: RunDone, Time: terminal.Time, Data: map[string]any{"state": terminal.State, "error": terminal.Error}}); err != nil {
		return err
	}
	kind := Complete
	data := map[string]any{"state": terminal.State, "thread_id": threadID}
	if terminal.State == "failed" {
		kind = RunError
		data = map[string]any{"state": terminal.State, "problem": map[string]any{"code": "run_failed", "message": "The run failed."}, "effect_ids": []int64{}}
	}
	if err := appendOne(ctx, tx, runID, runID, Payload{Kind: kind, Data: data, Time: terminal.Time}); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE run_streams SET terminal_seq=next_seq-1,terminal_at=? WHERE run_id=? AND terminal_seq IS NULL", terminal.Time, runID)
	return err
}

func Read(ctx context.Context, q db.Querier, runID, after int64, limit int) (Batch, error) {
	var b Batch
	var next int64
	err := q.QueryRowContext(ctx, "SELECT next_seq,retained_after,terminal_seq FROM run_streams WHERE run_id=?", runID).Scan(&next, &b.RetainedAfter, &b.TerminalSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return Batch{Next: 0}, nil
	}
	if err != nil {
		return b, err
	}
	b.Next = next - 1
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := q.QueryContext(ctx, "SELECT seq,run_id,kind,data_json FROM run_events WHERE stream_run_id=? AND seq>? ORDER BY seq LIMIT ?", runID, after, limit)
	if err != nil {
		return b, err
	}
	b.Events, err = db.Collect(rows, func(r *sql.Rows) (Event, error) {
		var e Event
		var raw string
		err := r.Scan(&e.ID, &e.RunID, &e.Kind, &raw)
		e.Data = json.RawMessage(raw)
		return e, err
	})
	return b, err
}

var subscriptions = struct {
	sync.Mutex
	listeners map[chan struct{}]struct{}
}{listeners: make(map[chan struct{}]struct{})}

func Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	subscriptions.Lock()
	subscriptions.listeners[ch] = struct{}{}
	subscriptions.Unlock()
	return ch, func() { subscriptions.Lock(); delete(subscriptions.listeners, ch); subscriptions.Unlock() }
}

func Notify() {
	subscriptions.Lock()
	defer subscriptions.Unlock()
	for ch := range subscriptions.listeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func Write(ctx context.Context, q db.Querier, fn func(*sql.Tx) error) error {
	if tx, ok := q.(*sql.Tx); ok {
		return fn(tx)
	}
	writer, ok := q.(*sql.DB)
	if !ok {
		return errors.New("progress: writer requires a database or transaction")
	}
	d := db.DB{Write: writer}
	err := d.WithTx(ctx, fn)
	if err == nil {
		Notify()
	}
	return err
}
