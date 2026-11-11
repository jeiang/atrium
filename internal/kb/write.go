package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tool"
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
	project, err := threads.ProjectOf(ctx, q, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tool.Error("this thread is gone")
	}
	return project, err
}

func threadMarks(ctx context.Context, q db.Querier, threadID int64) (tool.Marks, error) {
	marks, err := threads.MarksOfID(ctx, q, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, tool.Error("this thread is gone")
	}
	return marks, err
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
	first, err := journal.MinSeq(ctx, q, w.ThreadID, w.RunID)
	if err != nil {
		return 0, nil, nil, err
	}
	seq = first
	run := w.RunID
	return w.ThreadID, seq, &run, nil
}

func projectPrivate(ctx context.Context, q db.Querier, project *int64) (bool, error) {
	if project == nil {
		return false, nil
	}
	return projects.Private(ctx, q, *project)
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

func Write(ctx context.Context, store *ledger.Store, w Writer, in WriteInput) (Record, int64, error) {
	writer, err := w.name()
	if err != nil {
		return Record{}, 0, err
	}
	var out Record
	effect, err := store.ApplyLocal(ctx, ledger.New{
		Origin: "agent", Operation: "kb_write", OperationKey: "kb_write:" + ledger.NewKey(),
		Target: "kb:new", ThreadID: &w.ThreadID, RunID: &w.RunID,
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		project, err := threadProject(ctx, tx, w.ThreadID)
		if err != nil {
			return ledger.Result{}, err
		}
		marks, err := threadMarks(ctx, tx, w.ThreadID)
		if err != nil {
			return ledger.Result{}, err
		}
		if in.ID != 0 {
			rec, err := Get(ctx, tx, in.ID)
			if err != nil {
				return ledger.Result{}, err
			}
			if rec == nil {
				return ledger.Result{}, tool.Error(fmt.Sprintf("%s is unknown", Cite(in.ID)))
			}
			if err := refused(*rec, "edited"); err != nil {
				return ledger.Result{}, err
			}
			if projectOf(rec.ProjectID) != projectOf(project) {
				return ledger.Result{}, tool.Error(fmt.Sprintf("%s belongs to another project", Cite(in.ID)))
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
			aliases, err := aliasesJSON(next.Aliases)
			if err != nil {
				return ledger.Result{}, err
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE kb_records SET title = ?, aliases = ?, body = ?, scope = ?, marks = ?, updated_at = ? WHERE id = ?",
				next.Title, aliases, next.Body, next.Scope, int64(next.Marks), next.UpdatedAt, next.ID); err != nil {
				return ledger.Result{}, err
			}
			out = next
			return writeResult(next, beforeOf(*rec)), nil
		}
		if in.Title == "" || in.Body == "" {
			return ledger.Result{}, tool.Error("title and body are required to create a record")
		}
		scope := in.Scope
		if scope == "" {
			scope = ScopePersonal
		}
		if scope != ScopeWork && scope != ScopePersonal {
			return ledger.Result{}, tool.Error("scope must be work or personal")
		}
		found, err := duplicate(ctx, tx, scope, projectOf(project), w.Now, in.Title, in.Aliases)
		if err != nil {
			return ledger.Result{}, err
		}
		if found != 0 {
			return ledger.Result{}, tool.Error(fmt.Sprintf("%s exists; pass id to edit", Cite(found)))
		}
		sourceThread, sourceSeq, sourceRun, err := sourceOf(ctx, tx, w, in, project)
		if err != nil {
			return ledger.Result{}, err
		}
		aliases, err := aliasesJSON(in.Aliases)
		if err != nil {
			return ledger.Result{}, err
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO kb_records (title, aliases, body, scope, project_id, private, marks, writer, source_thread_id, source_seq, "+
				"source_run_id, pinned, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, 0, ?, ?)",
			in.Title, aliases, in.Body, scope, project, int64(marks), writer, sourceThread, sourceSeq, sourceRun, w.Now, w.Now)
		if err != nil {
			return ledger.Result{}, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return ledger.Result{}, err
		}
		rec, err := Get(ctx, tx, id)
		if err != nil {
			return ledger.Result{}, err
		}
		out = *rec
		return writeResult(*rec, nil), nil
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, effect.ID, nil
}

func Retract(ctx context.Context, store *ledger.Store, w Writer, id int64, reason string) (Record, int64, error) {
	var out Record
	effect, err := store.ApplyLocal(ctx, ledger.New{
		Origin: "agent", Operation: "kb_retract", OperationKey: "kb_retract:" + ledger.NewKey(),
		Target: "kb:" + fmt.Sprint(id), ThreadID: &w.ThreadID, RunID: &w.RunID,
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		rec, err := Get(ctx, tx, id)
		if err != nil {
			return ledger.Result{}, err
		}
		if rec == nil {
			return ledger.Result{}, tool.Error(fmt.Sprintf("%s is unknown", Cite(id)))
		}
		if err := refused(*rec, "retracted"); err != nil {
			return ledger.Result{}, err
		}
		project, err := threadProject(ctx, tx, w.ThreadID)
		if err != nil {
			return ledger.Result{}, err
		}
		if projectOf(rec.ProjectID) != projectOf(project) {
			return ledger.Result{}, tool.Error(fmt.Sprintf("%s belongs to another project", Cite(id)))
		}
		now := w.Now
		if _, err := tx.ExecContext(ctx, "UPDATE kb_records SET retracted_at = ?, embedded_hash = NULL, updated_at = ? WHERE id = ?",
			now, now, id); err != nil {
			return ledger.Result{}, err
		}
		if err := embed.DeleteKB(ctx, tx, id); err != nil {
			return ledger.Result{}, err
		}
		next := *rec
		next.RetractedAt, next.UpdatedAt = &now, now
		out = next
		r := writeResult(next, beforeOf(*rec))
		r.After = jsonOf(map[string]any{"retracted_at": now, "reason": reason})
		return r, nil
	})
	if err != nil {
		return Record{}, 0, err
	}
	return out, effect.ID, nil
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
		return nil, ledger.Refused("knowledge undo payload is invalid")
	}
	rec, err := Get(ctx, q, u.ID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, ledger.Refused(fmt.Sprintf("%s is gone", Cite(u.ID)))
	}
	if rec.UpdatedAt != u.Applied {
		return nil, ledger.Refused(fmt.Sprintf("%s changed since this write", Cite(u.ID)))
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if u.Before == nil {
		if _, err := q.ExecContext(ctx, "UPDATE kb_records SET retracted_at = ?, embedded_hash = NULL, updated_at = ? WHERE id = ?", now, now, u.ID); err != nil {
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
			"retracted_at = ?, embedded_hash = NULL, updated_at = ? WHERE id = ?",
		u.Before.Title, aliases, u.Before.Body, u.Before.Scope, u.Before.Marks, b2i(u.Before.Pinned), u.Before.ConfirmedAt,
		u.Before.RetractedAt, now, u.ID); err != nil {
		return nil, err
	}
	return map[string]any{"id": u.ID, "restored": true}, nil
}

func RegisterLedgerUndoers(s *ledger.Store) error {
	return s.RegisterUndo(UndoOp, ledger.Undoer{
		Validate: func(payload json.RawMessage) error {
			var u undoPayload
			if err := json.Unmarshal(payload, &u); err != nil {
				return ledger.Refused("knowledge undo payload is invalid")
			}
			if u.Op != UndoOp || u.ID == 0 {
				return ledger.Refused("knowledge undo payload names " + u.Op)
			}
			return nil
		},
		Local: func(ctx context.Context, tx *sql.Tx, payload json.RawMessage) (ledger.Result, error) {
			value, err := undo(ctx, tx, payload)
			if err != nil {
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
		},
	})
}
