package money

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

type Context struct {
	Now      func() float64
	ThreadID *int64
	RunID    *int64
}

type Row map[string]any

func (r Row) value() any {
	if r == nil {
		return nil
	}
	return map[string]any(r)
}

func rowOf(ctx context.Context, q db.Querier, table string, id int64) (Row, error) {
	rows, err := q.QueryContext(ctx, "SELECT * FROM "+table+" WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return nil, rows.Err()
	}
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]any, len(columns))
	targets := make([]any, len(columns))
	for i := range values {
		targets[i] = &values[i]
	}
	if err := rows.Scan(targets...); err != nil {
		return nil, err
	}
	row := make(Row, len(columns))
	for i, name := range columns {
		if b, ok := values[i].([]byte); ok {
			values[i] = string(b)
		}
		row[name] = values[i]
	}
	return row, nil
}

func rowsEqual(a, b Row) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || v != w {
			return false
		}
	}
	return true
}

func rowString(r Row, key string) string {
	s, _ := r[key].(string)
	return s
}

func rowInt(r Row, key string) int64 {
	switch v := r[key].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

type Change struct {
	before map[string]map[string]Row
}

func NewChange() *Change {
	before := make(map[string]map[string]Row, len(snapshotTables))
	for _, t := range snapshotTables {
		before[t] = map[string]Row{}
	}
	return &Change{before: before}
}

func (c *Change) Touch(ctx context.Context, q db.Querier, table string, ids ...int64) error {
	for _, id := range ids {
		key := strconv.FormatInt(id, 10)
		if _, seen := c.before[table][key]; seen {
			continue
		}
		row, err := rowOf(ctx, q, table, id)
		if err != nil {
			return err
		}
		c.before[table][key] = row
	}
	return nil
}

func (c *Change) Created(table string, id int64) {
	key := strconv.FormatInt(id, 10)
	if _, seen := c.before[table][key]; !seen {
		c.before[table][key] = nil
	}
}

func (c *Change) TouchTransaction(ctx context.Context, q db.Querier, txID int64) error {
	if err := c.Touch(ctx, q, "transactions", txID); err != nil {
		return err
	}
	ids, err := queryIDs(ctx, q, "SELECT id FROM transaction_sources WHERE transaction_id = ?", txID)
	if err != nil {
		return err
	}
	return c.Touch(ctx, q, "transaction_sources", ids...)
}

func (c *Change) Record(ctx context.Context, q db.Querier, mc Context, tool, target string) (*int64, error) {
	before := map[string]any{}
	after := map[string]any{}
	changed := false
	for _, table := range snapshotTables {
		beforeRows := map[string]any{}
		afterRows := map[string]any{}
		for key, was := range c.before[table] {
			id, err := strconv.ParseInt(key, 10, 64)
			if err != nil {
				return nil, err
			}
			now, err := rowOf(ctx, q, table, id)
			if err != nil {
				return nil, err
			}
			if rowsEqual(was, now) {
				continue
			}
			changed = true
			beforeRows[key] = was.value()
			afterRows[key] = now.value()
		}
		before[table] = beforeRows
		after[table] = afterRows
	}
	if !changed {
		return nil, nil
	}
	id, err := writes.Record(ctx, q, writes.New{
		TS: mc.Now(), ThreadID: mc.ThreadID, RunID: mc.RunID, Tool: tool, Target: target,
		Before: before, After: after,
		Undo: map[string]any{"op": UndoOp, "before": before, "after": after},
	})
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func RegisterUndoer(r *writes.Registry) { r.Register(UndoOp, undoRestore) }

func jsonRows(v any, table string) map[string]any {
	m, _ := v.(map[string]any)
	rows, _ := m[table].(map[string]any)
	return rows
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func matchesSnapshot(current Row, expected any) bool {
	if expected == nil {
		return current == nil
	}
	want, ok := expected.(map[string]any)
	if !ok || current == nil || len(want) != len(current) {
		return false
	}
	for k, w := range want {
		v, present := current[k]
		if !present {
			return false
		}
		if wn, isNum := numeric(w); isNum {
			vn, vIsNum := numeric(v)
			if !vIsNum || vn != wn {
				return false
			}
			continue
		}
		if v != w {
			return false
		}
	}
	return true
}

func bindable(v any) any {
	if f, ok := v.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return int64(f)
	}
	return v
}

func undoRestore(ctx context.Context, q db.Querier, info map[string]any) (any, error) {
	before, after := info["before"], info["after"]
	for _, table := range snapshotTables {
		expectedRows := jsonRows(after, table)
		for _, key := range sortedKeys(expectedRows) {
			id, err := strconv.ParseInt(key, 10, 64)
			if err != nil {
				return nil, err
			}
			current, err := rowOf(ctx, q, table, id)
			if err != nil {
				return nil, err
			}
			if !matchesSnapshot(current, expectedRows[key]) {
				return nil, writes.UndoError(fmt.Sprintf("%s %s changed since this write", table, key))
			}
		}
	}
	if _, err := q.ExecContext(ctx, "SAVEPOINT money_undo"); err != nil {
		return nil, err
	}
	if err := restoreRows(ctx, q, before); err != nil {
		_, _ = q.ExecContext(ctx, "ROLLBACK TO money_undo")
		_, _ = q.ExecContext(ctx, "RELEASE money_undo")
		return nil, err
	}
	if _, err := q.ExecContext(ctx, "RELEASE money_undo"); err != nil {
		return nil, err
	}
	restored := py.Dict{}
	beforeMap, _ := before.(map[string]any)
	for _, table := range sortedKeys(beforeMap) {
		if n := len(jsonRows(before, table)); n > 0 {
			restored = append(restored, py.Pair{Key: table, Value: int64(n)})
		}
	}
	return py.Dict{{Key: "restored", Value: restored}}, nil
}

func restoreRows(ctx context.Context, q db.Querier, before any) error {
	if _, err := q.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return err
	}
	for _, table := range slices.Backward(snapshotTables) {
		rows := jsonRows(before, table)
		for _, key := range sortedKeys(rows) {
			if rows[key] != nil {
				continue
			}
			id, err := strconv.ParseInt(key, 10, 64)
			if err != nil {
				return err
			}
			if _, err := q.ExecContext(ctx, "DELETE FROM "+table+" WHERE id = ?", id); err != nil {
				return err
			}
		}
	}
	for _, table := range snapshotTables {
		rows := jsonRows(before, table)
		for _, key := range sortedKeys(rows) {
			previous, _ := rows[key].(map[string]any)
			if previous == nil {
				continue
			}
			columns := sortedKeys(previous)
			values := make([]any, len(columns))
			for i, c := range columns {
				values[i] = bindable(previous[c])
			}
			query := fmt.Sprintf("INSERT OR REPLACE INTO %s (%s) VALUES (%s)", table, joinList(columns), placeholders(len(columns)))
			if _, err := q.ExecContext(ctx, query, values...); err != nil {
				return err
			}
		}
	}
	return nil
}
