package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/writes"
)

type Writer struct {
	ThreadID int64
	RunID    int64
	Kind     string
	Now      float64
}

func (w Writer) name() (string, error) {
	switch tool.Kind(w.Kind) {
	case tool.Chat, tool.Cron:
		return WriterChat, nil
	case tool.Nightly:
		return WriterDreaming, nil
	}
	return "", tool.Error("knowledge cannot be written from this run kind")
}

type Source struct {
	ThreadID int64
	Seq      *int64
}

type WriteInput struct {
	ID      int64
	Title   string
	Body    string
	Aliases []string
	Scope   string
	Source  *Source
}

type beforeState struct {
	Title       string   `json:"title"`
	Aliases     []string `json:"aliases"`
	Body        string   `json:"body"`
	Scope       string   `json:"scope"`
	Marks       int64    `json:"marks"`
	Pinned      bool     `json:"pinned"`
	ConfirmedAt *float64 `json:"confirmed_at"`
	RetractedAt *float64 `json:"retracted_at"`
}

type undoPayload struct {
	Op      string       `json:"op"`
	ID      int64        `json:"id"`
	Before  *beforeState `json:"before"`
	Applied float64      `json:"applied_updated_at"`
}

func beforeOf(r Record) *beforeState {
	return &beforeState{
		Title: r.Title, Aliases: r.Aliases, Body: r.Body, Scope: r.Scope,
		Marks: int64(r.Marks), Pinned: r.Pinned, ConfirmedAt: r.ConfirmedAt, RetractedAt: r.RetractedAt,
	}
}

func stateMap(r Record) map[string]any {
	return map[string]any{
		"title": r.Title, "aliases": r.Aliases, "body": r.Body, "scope": r.Scope,
		"marks": int64(r.Marks), "pinned": r.Pinned, "confirmed_at": r.ConfirmedAt,
		"retracted_at": r.RetractedAt, "writer": r.Writer,
	}
}

func threadProject(ctx context.Context, q db.Querier, threadID int64) (*int64, error) {
	var project *int64
	err := q.QueryRowContext(ctx, "SELECT project_id FROM threads WHERE id = ? AND deleted_at IS NULL", threadID).Scan(&project)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tool.Error("this thread is gone")
	}
	return project, err
}

func threadMarks(ctx context.Context, q db.Querier, threadID int64) (tool.Marks, error) {
	var marks int64
	err := q.QueryRowContext(ctx, "SELECT marks FROM threads WHERE id = ?", threadID).Scan(&marks)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, tool.Error("this thread is gone")
	}
	return tool.Marks(marks), err
}

func sourceOf(ctx context.Context, q db.Querier, w Writer, in WriteInput, project *int64) (threadID int64, seq, runID *int64, err error) {
	if in.Source != nil {
		srcProject, err := threadProject(ctx, q, in.Source.ThreadID)
		if err != nil {
			return 0, nil, nil, tool.Error(fmt.Sprintf("source thread %d is unknown or deleted", in.Source.ThreadID))
		}
		private, err := projectPrivate(ctx, q, srcProject)
		if err != nil {
			return 0, nil, nil, err
		}
		if private && projectOf(srcProject) != projectOf(project) {
			return 0, nil, nil, tool.Error(fmt.Sprintf("source thread %d belongs to a private project", in.Source.ThreadID))
		}
		run := w.RunID
		return in.Source.ThreadID, in.Source.Seq, &run, nil
	}
	var first sql.NullInt64
	if err := q.QueryRowContext(ctx, "SELECT MIN(seq) FROM thread_messages WHERE thread_id = ? AND run_id = ?",
		w.ThreadID, w.RunID).Scan(&first); err != nil {
		return 0, nil, nil, err
	}
	if first.Valid {
		seq = &first.Int64
	}
	run := w.RunID
	return w.ThreadID, seq, &run, nil
}

func projectPrivate(ctx context.Context, q db.Querier, project *int64) (bool, error) {
	if project == nil {
		return false, nil
	}
	var private int64
	err := q.QueryRowContext(ctx, "SELECT private FROM projects WHERE id = ?", *project).Scan(&private)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return private != 0, err
}

func existing(ctx context.Context, q db.Querier, scope string, project int64, now float64) ([]exactRow, error) {
	where, args := liveWhere("r", project, now, scope)
	rows, err := q.QueryContext(ctx, "SELECT r.id, r.title, r.aliases"+fromClause()+" WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (exactRow, error) {
		var e exactRow
		err := r.Scan(&e.id, &e.title, &e.aliases)
		return e, err
	})
}

func duplicate(ctx context.Context, q db.Querier, scope string, project int64, now float64, title string, aliases []string) (int64, error) {
	list, err := existing(ctx, q, scope, project, now)
	if err != nil {
		return 0, err
	}
	wanted := map[string]bool{normalize(title): true}
	for _, alias := range aliases {
		wanted[normalize(alias)] = true
	}
	for _, e := range list {
		var have []string
		_ = json.Unmarshal([]byte(e.aliases), &have)
		for _, candidate := range append([]string{e.title}, have...) {
			if wanted[normalize(candidate)] {
				return e.id, nil
			}
		}
	}
	return 0, nil
}

func refused(r Record, verb string) error {
	switch {
	case r.Confirmed():
		return tool.Error(fmt.Sprintf("%s is confirmed by the operator and cannot be %s", Cite(r.ID), verb))
	case r.Retracted():
		return tool.Error(fmt.Sprintf("%s is retracted and cannot be %s", Cite(r.ID), verb))
	case r.Writer == WriterOperator || r.Writer == WriterImport:
		return tool.Error(fmt.Sprintf("%s is operator-owned and cannot be %s", Cite(r.ID), verb))
	case r.Private && r.ProjectID == nil:
		return tool.Error(fmt.Sprintf("%s is private and cannot be %s", Cite(r.ID), verb))
	}
	return nil
}

func Write(ctx context.Context, d *db.DB, reg *writes.Registry, w Writer, in WriteInput) (Record, int64, error) {
	writer, err := w.name()
	if err != nil {
		return Record{}, 0, err
	}
	var out Record
	var writeID int64
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		project, err := threadProject(ctx, tx, w.ThreadID)
		if err != nil {
			return err
		}
		marks, err := threadMarks(ctx, tx, w.ThreadID)
		if err != nil {
			return err
		}
		if in.ID != 0 {
			rec, err := Get(ctx, tx, in.ID)
			if err != nil {
				return err
			}
			if rec == nil {
				return tool.Error(fmt.Sprintf("%s is unknown", Cite(in.ID)))
			}
			if err := refused(*rec, "edited"); err != nil {
				return err
			}
			if projectOf(rec.ProjectID) != projectOf(project) {
				return tool.Error(fmt.Sprintf("%s belongs to another project", Cite(in.ID)))
			}
			next := *rec
			if in.Title != "" {
				next.Title = in.Title
			}
			if in.Body != "" {
				next.Body = in.Body
			}
			if in.Aliases != nil {
				next.Aliases = normalizeAliases(in.Aliases)
			}
			if in.Scope != "" {
				next.Scope = in.Scope
			}
			next.Marks = rec.Marks | marks
			next.UpdatedAt = w.Now
			if next.Title != rec.Title || next.Body != rec.Body || strings.Join(next.Aliases, "\x00") != strings.Join(rec.Aliases, "\x00") {
				next.Embedded = false
			}
			aliases, err := aliasesJSON(next.Aliases)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE kb_records SET title = ?, aliases = ?, body = ?, scope = ?, marks = ?, embedded = ?, updated_at = ? WHERE id = ?",
				next.Title, aliases, next.Body, next.Scope, int64(next.Marks), b2i(next.Embedded), next.UpdatedAt, next.ID); err != nil {
				return err
			}
			next.Embedded = b2i(next.Embedded) != 0
			out = next
			writeID, err = writes.Record(ctx, tx, reg, writes.New{
				TS: w.Now, ThreadID: &w.ThreadID, RunID: &w.RunID, Tool: "kb_write", Target: "kb:" + fmt.Sprint(next.ID),
				Before: stateMap(*rec), After: stateMap(next),
				Undo: undoPayload{Op: UndoOp, ID: next.ID, Before: beforeOf(*rec), Applied: next.UpdatedAt},
			})
			return err
		}
		if in.Title == "" || in.Body == "" {
			return tool.Error("title and body are required to create a record")
		}
		scope := in.Scope
		if scope == "" {
			scope = ScopePersonal
		}
		if scope != ScopeWork && scope != ScopePersonal {
			return tool.Error("scope must be work or personal")
		}
		found, err := duplicate(ctx, tx, scope, projectOf(project), w.Now, in.Title, in.Aliases)
		if err != nil {
			return err
		}
		if found != 0 {
			return tool.Error(fmt.Sprintf("%s exists; pass id to edit", Cite(found)))
		}
		sourceThread, sourceSeq, sourceRun, err := sourceOf(ctx, tx, w, in, project)
		if err != nil {
			return err
		}
		aliases, err := aliasesJSON(in.Aliases)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO kb_records (title, aliases, body, scope, project_id, private, marks, writer, source_thread_id, source_seq, "+
				"source_run_id, pinned, embedded, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, 0, 0, ?, ?)",
			in.Title, aliases, in.Body, scope, project, int64(marks), writer, sourceThread, sourceSeq, sourceRun, w.Now, w.Now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		rec, err := Get(ctx, tx, id)
		if err != nil {
			return err
		}
		out = *rec
		writeID, err = writes.Record(ctx, tx, reg, writes.New{
			TS: w.Now, ThreadID: &w.ThreadID, RunID: &w.RunID, Tool: "kb_write", Target: "kb:" + fmt.Sprint(id),
			After: stateMap(*rec),
			Undo:  undoPayload{Op: UndoOp, ID: id, Applied: rec.UpdatedAt},
		})
		return err
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, writeID, nil
}

func UpsertFact(ctx context.Context, d *db.DB, origin, title, body string, expires, now float64) (Record, error) {
	if origin == "" || title == "" || body == "" {
		return Record{}, errors.New("kb: a fact needs an origin, title and body")
	}
	var out Record
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx,
			"INSERT INTO kb_records (title, aliases, body, scope, project_id, private, marks, writer, origin, pinned, embedded, expires_at, created_at, updated_at) "+
				"VALUES (?, '[]', ?, ?, NULL, 0, 0, ?, ?, 0, 0, ?, ?, ?) "+
				"ON CONFLICT(origin) DO UPDATE SET title = excluded.title, body = excluded.body, expires_at = excluded.expires_at, "+
				"retracted_at = NULL, embedded = 0, updated_at = excluded.updated_at RETURNING id",
			title, body, ScopeOps, WriterWorker, origin, expires, now, now).Scan(&id)
		if err != nil {
			return err
		}
		rec, err := Get(ctx, tx, id)
		if err != nil {
			return err
		}
		out = *rec
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return out, nil
}

func UpsertFinding(ctx context.Context, d *db.DB, reg *writes.Registry, w Writer, origin, title, body, severity string, marks tool.Marks, now float64) (Record, int64, error) {
	if origin == "" || title == "" || body == "" {
		return Record{}, 0, errors.New("kb: a finding needs an origin, title and body")
	}
	if severity != "info" && severity != "warning" && severity != "critical" {
		return Record{}, 0, errors.New("kb: a finding severity must be info, warning or critical")
	}
	aliases, err := aliasesJSON([]string{"severity:" + severity})
	if err != nil {
		return Record{}, 0, err
	}
	var out Record
	var writeID int64
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx,
			"INSERT INTO kb_records (title, aliases, body, scope, project_id, private, marks, writer, origin, pinned, embedded, expires_at, created_at, updated_at, source_thread_id, source_run_id) "+
				"VALUES (?, ?, ?, ?, NULL, 0, ?, ?, ?, 0, 0, NULL, ?, ?, ?, ?) "+
				"ON CONFLICT(origin) DO UPDATE SET title = excluded.title, aliases = excluded.aliases, body = excluded.body, marks = excluded.marks, "+
				"retracted_at = NULL, embedded = 0, updated_at = excluded.updated_at RETURNING id",
			title, aliases, body, ScopeOps, int64(marks), WriterWorker, origin, now, now, w.ThreadID, w.RunID).Scan(&id)
		if err != nil {
			return err
		}
		rec, err := Get(ctx, tx, id)
		if err != nil {
			return err
		}
		out = *rec
		writeID, err = writes.Record(ctx, tx, reg, writes.New{
			TS: now, ThreadID: &w.ThreadID, RunID: &w.RunID, Tool: "ops_finding", Target: "kb:" + fmt.Sprint(id),
			After: map[string]any{"title": title, "body": body, "severity": severity, "fact": origin},
			Undo:  undoPayload{Op: UndoOp, ID: id, Applied: now},
		})
		return err
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, writeID, nil
}

func RetractFact(ctx context.Context, d *db.DB, id int64, now float64) (Record, error) {
	var out Record
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		rec, err := Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if rec == nil {
			return tool.Error(fmt.Sprintf("%s is unknown", Cite(id)))
		}
		if rec.Retracted() {
			out = *rec
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE kb_records SET retracted_at = ?, embedded = 0, updated_at = ? WHERE id = ?",
			now, now, id); err != nil {
			return err
		}
		if err := embed.DeleteKB(ctx, tx, id); err != nil {
			return err
		}
		next := *rec
		next.RetractedAt, next.Embedded, next.UpdatedAt = &now, false, now
		out = next
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return out, nil
}

func Retract(ctx context.Context, d *db.DB, reg *writes.Registry, w Writer, id int64, reason string) (Record, int64, error) {
	var out Record
	var writeID int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		rec, err := Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if rec == nil {
			return tool.Error(fmt.Sprintf("%s is unknown", Cite(id)))
		}
		if err := refused(*rec, "retracted"); err != nil {
			return err
		}
		now := w.Now
		if _, err := tx.ExecContext(ctx, "UPDATE kb_records SET retracted_at = ?, embedded = 0, updated_at = ? WHERE id = ?",
			now, now, id); err != nil {
			return err
		}
		if err := embed.DeleteKB(ctx, tx, id); err != nil {
			return err
		}
		next := *rec
		next.RetractedAt, next.Embedded, next.UpdatedAt = &now, false, now
		out = next
		writeID, err = writes.Record(ctx, tx, reg, writes.New{
			TS: now, ThreadID: &w.ThreadID, RunID: &w.RunID, Tool: "kb_retract", Target: "kb:" + fmt.Sprint(id),
			Before: stateMap(*rec), After: map[string]any{"retracted_at": now, "reason": reason},
			Undo: undoPayload{Op: UndoOp, ID: id, Before: beforeOf(*rec), Applied: now},
		})
		return err
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, writeID, nil
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func undo(ctx context.Context, q db.Querier, payload json.RawMessage) (any, error) {
	var u undoPayload
	if err := json.Unmarshal(payload, &u); err != nil {
		return nil, writes.UndoError("knowledge undo payload is invalid")
	}
	rec, err := Get(ctx, q, u.ID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, writes.UndoError(fmt.Sprintf("%s is gone", Cite(u.ID)))
	}
	if rec.UpdatedAt != u.Applied {
		return nil, writes.UndoError(fmt.Sprintf("%s changed since this write", Cite(u.ID)))
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if u.Before == nil {
		if _, err := q.ExecContext(ctx, "UPDATE kb_records SET retracted_at = ?, embedded = 0, updated_at = ? WHERE id = ?", now, now, u.ID); err != nil {
			return nil, err
		}
		if err := embed.DeleteKB(ctx, q, u.ID); err != nil {
			return nil, err
		}
		return map[string]any{"id": u.ID, "retracted": true}, nil
	}
	aliases, err := aliasesJSON(u.Before.Aliases)
	if err != nil {
		return nil, err
	}
	if _, err := q.ExecContext(ctx,
		"UPDATE kb_records SET title = ?, aliases = ?, body = ?, scope = ?, marks = ?, pinned = ?, confirmed_at = ?, "+
			"retracted_at = ?, embedded = 0, updated_at = ? WHERE id = ?",
		u.Before.Title, aliases, u.Before.Body, u.Before.Scope, u.Before.Marks, b2i(u.Before.Pinned), u.Before.ConfirmedAt,
		u.Before.RetractedAt, now, u.ID); err != nil {
		return nil, err
	}
	return map[string]any{"id": u.ID, "restored": true}, nil
}

func RegisterUndoer(r *writes.Registry) { r.Register(UndoOp, undo) }
