package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tool"
)

type Refused string

func (e Refused) Error() string { return string(e) }

type EditInput struct {
	Title   string
	Body    string
	Aliases []string
	Scope   string
	Private bool
	Marks   tool.Marks
	Pinned  bool
}

func validScope(scope string) bool {
	return scope == ScopeWork || scope == ScopePersonal
}

func stateOf(r Record) map[string]any { return stateMap(r) }

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func writeResult(rec Record, before *beforeState) ledger.Result {
	r := ledger.Result{State: ledger.Done, Target: "kb:" + fmt.Sprint(rec.ID), After: jsonOf(stateOf(rec))}
	if before != nil {
		r.Before = jsonOf(stateOf(Record{
			Title: before.Title, Aliases: before.Aliases, Body: before.Body, Scope: before.Scope,
			Marks: tool.Marks(before.Marks), Pinned: before.Pinned, ConfirmedAt: before.ConfirmedAt, RetractedAt: before.RetractedAt,
		}))
	}
	r.Undo = jsonOf(undoPayload{Op: UndoOp, ID: rec.ID, Before: before, Applied: rec.UpdatedAt})
	return r
}

func checkPin(ctx context.Context, q db.Querier, id int64) error {
	budget := int64(assistant.TuningAgentKbProfileTokens)
	entries, err := ProfileReview(ctx, q, budget)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.ID != id {
			continue
		}
		if entry.Reason == "over budget" {
			return Refused("the profile is full: over budget")
		}
		return nil
	}
	return nil
}

func load(ctx context.Context, q db.Querier, id int64) (Record, error) {
	rec, err := Get(ctx, q, id)
	if err != nil {
		return Record{}, err
	}
	if rec == nil {
		return Record{}, Refused(fmt.Sprintf("%s is unknown", Cite(id)))
	}
	return *rec, nil
}

func editChanges(rec Record, in EditInput) bool {
	return rec.Title != in.Title || rec.Body != in.Body || rec.Scope != in.Scope ||
		rec.Private != in.Private || rec.Marks != in.Marks || rec.Pinned != in.Pinned ||
		fmt.Sprint(rec.Aliases) != fmt.Sprint(normalizeAliases(in.Aliases))
}

func OperatorEdit(ctx context.Context, store *ledger.Store, id int64, in EditInput, now float64) (Record, int64, error) {
	if !validScope(in.Scope) {
		return Record{}, 0, Refused("scope must be work or personal")
	}
	current, err := load(ctx, store.DB.Read, id)
	if err != nil {
		return Record{}, 0, err
	}
	if !editChanges(current, in) {
		return current, 0, nil
	}
	var out Record
	effect, err := store.ApplyLocal(ctx, ledger.New{
		Origin: "local", Operation: "kb_edit", OperationKey: "kb_edit:" + ledger.NewKey(), Target: "kb:" + fmt.Sprint(id),
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		rec, err := load(ctx, tx, id)
		if err != nil {
			return ledger.Result{}, err
		}
		if !editChanges(rec, in) {
			out = rec
			return ledger.Result{State: ledger.Skipped}, nil
		}
		aliases, err := aliasesJSON(in.Aliases)
		if err != nil {
			return ledger.Result{}, err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE kb_records SET title = ?, aliases = ?, body = ?, scope = ?, private = ?, marks = ?, pinned = ?, updated_at = ? WHERE id = ?",
			in.Title, aliases, in.Body, in.Scope, b2i(in.Private), int64(in.Marks), b2i(in.Pinned), now, id); err != nil {
			return ledger.Result{}, err
		}
		if in.Pinned && !rec.Pinned {
			if err := checkPin(ctx, tx, id); err != nil {
				return ledger.Result{}, err
			}
		}
		next := rec
		next.Title, next.Body, next.Scope, next.Private, next.Marks, next.Pinned = in.Title, in.Body, in.Scope, in.Private, in.Marks, in.Pinned
		next.Aliases, next.UpdatedAt = normalizeAliases(in.Aliases), now
		out = next
		return writeResult(next, beforeOf(rec)), nil
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, effect.ID, nil
}

func OperatorCreate(ctx context.Context, store *ledger.Store, in EditInput, now float64) (Record, int64, error) {
	if in.Title == "" || in.Body == "" {
		return Record{}, 0, Refused("title and body are required")
	}
	if !validScope(in.Scope) {
		return Record{}, 0, Refused("scope must be work or personal")
	}
	var out Record
	effect, err := store.ApplyLocal(ctx, ledger.New{
		Origin: "local", Operation: "kb_write", OperationKey: "kb_write:" + ledger.NewKey(), Target: "kb:new",
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		aliases, err := aliasesJSON(in.Aliases)
		if err != nil {
			return ledger.Result{}, err
		}
		confirmed := any(nil)
		if in.Pinned {
			confirmed = now
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO kb_records (title, aliases, body, scope, private, marks, writer, pinned, confirmed_at, created_at, updated_at) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			in.Title, aliases, in.Body, in.Scope, b2i(in.Private), int64(in.Marks), WriterOperator, b2i(in.Pinned), confirmed, now, now)
		if err != nil {
			return ledger.Result{}, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return ledger.Result{}, err
		}
		rec, err := load(ctx, tx, id)
		if err != nil {
			return ledger.Result{}, err
		}
		if in.Pinned {
			if err := checkPin(ctx, tx, id); err != nil {
				return ledger.Result{}, err
			}
		}
		out = rec
		return writeResult(rec, nil), nil
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, effect.ID, nil
}

func Confirm(ctx context.Context, store *ledger.Store, id int64, now float64) (Record, int64, error) {
	current, err := load(ctx, store.DB.Read, id)
	if err != nil {
		return Record{}, 0, err
	}
	if current.Retracted() {
		return Record{}, 0, Refused(fmt.Sprintf("%s is retracted", Cite(id)))
	}
	if current.Confirmed() {
		return current, 0, nil
	}
	var out Record
	effect, err := store.ApplyLocal(ctx, ledger.New{
		Origin: "local", Operation: "kb_confirm", OperationKey: "kb_confirm:" + ledger.NewKey(), Target: "kb:" + fmt.Sprint(id),
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		rec, err := load(ctx, tx, id)
		if err != nil {
			return ledger.Result{}, err
		}
		if rec.Retracted() {
			return ledger.Result{}, Refused(fmt.Sprintf("%s is retracted", Cite(id)))
		}
		if rec.Confirmed() {
			out = rec
			return ledger.Result{State: ledger.Skipped}, nil
		}
		marks := rec.Marks &^ tool.Untrusted
		if _, err := tx.ExecContext(ctx, "UPDATE kb_records SET confirmed_at = ?, marks = ?, updated_at = ? WHERE id = ?",
			now, int64(marks), now, id); err != nil {
			return ledger.Result{}, err
		}
		next := rec
		next.ConfirmedAt, next.Marks, next.UpdatedAt = &now, marks, now
		out = next
		return writeResult(next, beforeOf(rec)), nil
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, effect.ID, nil
}

func OperatorRetract(ctx context.Context, store *ledger.Store, id int64, now float64) (Record, int64, error) {
	current, err := load(ctx, store.DB.Read, id)
	if err != nil {
		return Record{}, 0, err
	}
	if current.Retracted() {
		return current, 0, nil
	}
	var out Record
	effect, err := store.ApplyLocal(ctx, ledger.New{
		Origin: "local", Operation: "kb_retract", OperationKey: "kb_retract:" + ledger.NewKey(), Target: "kb:" + fmt.Sprint(id),
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		rec, err := load(ctx, tx, id)
		if err != nil {
			return ledger.Result{}, err
		}
		if rec.Retracted() {
			out = rec
			return ledger.Result{State: ledger.Skipped}, nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE kb_records SET retracted_at = ?, embedded_hash = NULL, updated_at = ? WHERE id = ?", now, now, id); err != nil {
			return ledger.Result{}, err
		}
		if err := embed.DeleteKB(ctx, tx, id); err != nil {
			return ledger.Result{}, err
		}
		next := rec
		next.RetractedAt, next.UpdatedAt = &now, now
		out = next
		return writeResult(next, beforeOf(rec)), nil
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, effect.ID, nil
}

func Restore(ctx context.Context, store *ledger.Store, id int64, now float64) (Record, int64, error) {
	current, err := load(ctx, store.DB.Read, id)
	if err != nil {
		return Record{}, 0, err
	}
	if !current.Retracted() {
		return current, 0, nil
	}
	var out Record
	effect, err := store.ApplyLocal(ctx, ledger.New{
		Origin: "local", Operation: "kb_restore", OperationKey: "kb_restore:" + ledger.NewKey(), Target: "kb:" + fmt.Sprint(id),
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		rec, err := load(ctx, tx, id)
		if err != nil {
			return ledger.Result{}, err
		}
		if !rec.Retracted() {
			out = rec
			return ledger.Result{State: ledger.Skipped}, nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE kb_records SET retracted_at = NULL, embedded_hash = NULL, updated_at = ? WHERE id = ?", now, id); err != nil {
			return ledger.Result{}, err
		}
		next := rec
		next.RetractedAt, next.UpdatedAt = nil, now
		out = next
		return writeResult(next, beforeOf(rec)), nil
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, effect.ID, nil
}

func RunRetract(ctx context.Context, store *ledger.Store, runID int64, now float64) ([]Record, error) {
	rows, err := store.DB.Read.QueryContext(ctx, "SELECT id FROM kb_records WHERE source_run_id = ? AND retracted_at IS NULL ORDER BY id", runID)
	if err != nil {
		return nil, err
	}
	ids, err := db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		err := r.Scan(&id)
		return id, err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(ids))
	for _, id := range ids {
		rec, _, err := OperatorRetract(ctx, store, id, now)
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func Purge(ctx context.Context, store *ledger.Store, id int64, now float64) error {
	_, err := store.ApplyLocal(ctx, ledger.New{
		Origin: "local", Operation: "kb_purge", OperationKey: "kb_purge:" + ledger.NewKey(), Target: "kb:" + fmt.Sprint(id),
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		rec, err := Get(ctx, tx, id)
		if err != nil {
			return ledger.Result{}, err
		}
		if rec == nil {
			return ledger.Result{}, Refused(fmt.Sprintf("%s is unknown", Cite(id)))
		}
		if err := embed.DeleteKB(ctx, tx, id); err != nil {
			return ledger.Result{}, err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM kb_records WHERE id = ?", id); err != nil {
			return ledger.Result{}, err
		}
		if err := ledger.PurgeTarget(ctx, tx, "kb:"+fmt.Sprint(id)); err != nil {
			return ledger.Result{}, err
		}
		return ledger.Result{State: ledger.Done, Target: "kb:" + fmt.Sprint(id)}, nil
	})
	return err
}

func ErrUnknown(err error) bool {
	var refused Refused
	return errors.As(err, &refused)
}
