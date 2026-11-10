package kb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/writes"
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
	return scope == ScopeWork || scope == ScopePersonal || scope == ScopeOps
}

func stateOf(r Record) map[string]any { return stateMap(r) }

func recordWrite(ctx context.Context, tx *sql.Tx, reg *writes.Registry, toolName string, rec Record, before *beforeState, now float64) (int64, error) {
	w := writes.New{TS: now, Tool: toolName, Target: "kb:" + fmt.Sprint(rec.ID), After: stateOf(rec)}
	if before != nil {
		w.Before = stateOf(Record{
			Title: before.Title, Aliases: before.Aliases, Body: before.Body, Scope: before.Scope,
			Marks: tool.Marks(before.Marks), Pinned: before.Pinned, ConfirmedAt: before.ConfirmedAt, RetractedAt: before.RetractedAt,
		})
	}
	w.Undo = undoPayload{Op: UndoOp, ID: rec.ID, Before: before, Applied: rec.UpdatedAt}
	return writes.Record(ctx, tx, reg, w)
}

func checkPin(ctx context.Context, q db.Querier, id int64) error {
	r := assistant.Read(ctx, q)
	budget := int64(r.Int(assistant.KeyKBProfileTokens))
	if err := r.Err(); err != nil {
		return err
	}
	entries, err := ProfileEntries(ctx, q, budget)
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

func OperatorEdit(ctx context.Context, d *db.DB, reg *writes.Registry, id int64, in EditInput, now float64) (Record, int64, error) {
	if !validScope(in.Scope) {
		return Record{}, 0, Refused("scope must be work, personal or ops")
	}
	var out Record
	var writeID int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		rec, err := load(ctx, tx, id)
		if err != nil {
			return err
		}
		aliases, err := aliasesJSON(in.Aliases)
		if err != nil {
			return err
		}
		changed := rec.Title != in.Title || rec.Body != in.Body || rec.Scope != in.Scope ||
			rec.Private != in.Private || rec.Marks != in.Marks || rec.Pinned != in.Pinned ||
			fmt.Sprint(rec.Aliases) != fmt.Sprint(normalizeAliases(in.Aliases))
		if !changed {
			out = rec
			return nil
		}
		embedded := rec.Embedded
		if rec.Title != in.Title || rec.Body != in.Body || fmt.Sprint(rec.Aliases) != fmt.Sprint(normalizeAliases(in.Aliases)) {
			embedded = false
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE kb_records SET title = ?, aliases = ?, body = ?, scope = ?, private = ?, marks = ?, pinned = ?, embedded = ?, updated_at = ? WHERE id = ?",
			in.Title, aliases, in.Body, in.Scope, b2i(in.Private), int64(in.Marks), b2i(in.Pinned), b2i(embedded), now, id); err != nil {
			return err
		}
		if in.Pinned && !rec.Pinned {
			if err := checkPin(ctx, tx, id); err != nil {
				return err
			}
		}
		next := rec
		next.Title, next.Body, next.Scope, next.Private, next.Marks, next.Pinned = in.Title, in.Body, in.Scope, in.Private, in.Marks, in.Pinned
		next.Aliases, next.Embedded, next.UpdatedAt = normalizeAliases(in.Aliases), embedded, now
		out = next
		writeID, err = recordWrite(ctx, tx, reg, "kb_edit", next, beforeOf(rec), now)
		return err
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, writeID, nil
}

func OperatorCreate(ctx context.Context, d *db.DB, reg *writes.Registry, in EditInput, now float64) (Record, int64, error) {
	if in.Title == "" || in.Body == "" {
		return Record{}, 0, Refused("title and body are required")
	}
	if !validScope(in.Scope) {
		return Record{}, 0, Refused("scope must be work, personal or ops")
	}
	var out Record
	var writeID int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		aliases, err := aliasesJSON(in.Aliases)
		if err != nil {
			return err
		}
		confirmed := any(nil)
		if in.Pinned {
			confirmed = now
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO kb_records (title, aliases, body, scope, private, marks, writer, pinned, confirmed_at, embedded, created_at, updated_at) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)",
			in.Title, aliases, in.Body, in.Scope, b2i(in.Private), int64(in.Marks), WriterOperator, b2i(in.Pinned), confirmed, now, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		rec, err := load(ctx, tx, id)
		if err != nil {
			return err
		}
		if in.Pinned {
			if err := checkPin(ctx, tx, id); err != nil {
				return err
			}
		}
		out = rec
		writeID, err = recordWrite(ctx, tx, reg, "kb_write", rec, nil, now)
		return err
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, writeID, nil
}

func Confirm(ctx context.Context, d *db.DB, reg *writes.Registry, id int64, now float64) (Record, int64, error) {
	var out Record
	var writeID int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		rec, err := load(ctx, tx, id)
		if err != nil {
			return err
		}
		if rec.Retracted() {
			return Refused(fmt.Sprintf("%s is retracted", Cite(id)))
		}
		if rec.Confirmed() {
			out = rec
			return nil
		}
		marks := rec.Marks &^ tool.Untrusted
		if _, err := tx.ExecContext(ctx, "UPDATE kb_records SET confirmed_at = ?, marks = ?, updated_at = ? WHERE id = ?",
			now, int64(marks), now, id); err != nil {
			return err
		}
		next := rec
		next.ConfirmedAt, next.Marks, next.UpdatedAt = &now, marks, now
		out = next
		writeID, err = recordWrite(ctx, tx, reg, "kb_confirm", next, beforeOf(rec), now)
		return err
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, writeID, nil
}

func OperatorRetract(ctx context.Context, d *db.DB, reg *writes.Registry, id int64, now float64) (Record, int64, error) {
	var out Record
	var writeID int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		rec, err := load(ctx, tx, id)
		if err != nil {
			return err
		}
		if rec.Retracted() {
			out = rec
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE kb_records SET retracted_at = ?, embedded = 0, updated_at = ? WHERE id = ?", now, now, id); err != nil {
			return err
		}
		if err := embed.DeleteKB(ctx, tx, id); err != nil {
			return err
		}
		next := rec
		next.RetractedAt, next.Embedded, next.UpdatedAt = &now, false, now
		out = next
		writeID, err = recordWrite(ctx, tx, reg, "kb_retract", next, beforeOf(rec), now)
		return err
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, writeID, nil
}

func Restore(ctx context.Context, d *db.DB, reg *writes.Registry, id int64, now float64) (Record, int64, error) {
	var out Record
	var writeID int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		rec, err := load(ctx, tx, id)
		if err != nil {
			return err
		}
		if !rec.Retracted() {
			out = rec
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE kb_records SET retracted_at = NULL, embedded = 0, updated_at = ? WHERE id = ?", now, id); err != nil {
			return err
		}
		next := rec
		next.RetractedAt, next.Embedded, next.UpdatedAt = nil, false, now
		out = next
		writeID, err = recordWrite(ctx, tx, reg, "kb_restore", next, beforeOf(rec), now)
		return err
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, writeID, nil
}

func RunRetract(ctx context.Context, d *db.DB, reg *writes.Registry, runID int64, now float64) ([]Record, error) {
	rows, err := d.Read.QueryContext(ctx, "SELECT id FROM kb_records WHERE source_run_id = ? AND retracted_at IS NULL ORDER BY id", runID)
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
		rec, _, err := OperatorRetract(ctx, d, reg, id, now)
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func Purge(ctx context.Context, d *db.DB, id int64, now float64) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		rec, err := Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if rec == nil {
			return Refused(fmt.Sprintf("%s is unknown", Cite(id)))
		}
		if err := embed.DeleteKB(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM kb_records WHERE id = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE agent_writes SET before_json = '{\"purged\":true}', after_json = '{\"purged\":true}', undo_json = '{\"purged\":true}' WHERE target = ?",
			"kb:"+fmt.Sprint(id)); err != nil {
			return err
		}
		_, err = writes.Record(ctx, tx, nil, writes.New{TS: now, Tool: "kb_purge", Target: "kb:" + fmt.Sprint(id)})
		return err
	})
}

func ErrUnknown(err error) bool {
	var refused Refused
	return errors.As(err, &refused)
}
