package reviewpages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	categoryAssistant = "assistant"
	categoryMoney     = "money"
	categoryWiki      = "wiki"
	categoryMedia     = "media"
	pageSize          = 50
	snippetLimit      = 800
)

var categories = []string{categoryMail, categoryAssistant, categoryMoney, categoryWiki, categoryMedia}

const categorySQL = "CASE WHEN json_extract(w.undo_json, '$.op') = 'money_restore' THEN 'money' WHEN w.tool LIKE 'notion%' THEN 'wiki' " +
	"WHEN w.tool LIKE 'media%' THEN 'media' ELSE 'assistant' END"

const writeColumns = "id, ts, thread_id, tool, target, before_json, after_json, undo_json, undone_at, thread_kind, category"

// snippet is _snippet: the JSON text of a stored value, indented, cut at 800 characters.
func snippet(raw *string) (string, error) {
	if raw == nil || *raw == "" {
		return "", nil
	}
	value, err := py.JSONLoads([]byte(*raw))
	if err != nil {
		return "", err
	}
	text := []rune(py.JSONDumps(value, py.JSONOpts{Indent: 1, NoEnsureASCII: true}))
	if len(text) <= snippetLimit {
		return string(text), nil
	}
	return string(text[:snippetLimit]) + "…", nil
}

func scanWrite(sc interface{ Scan(...any) error }) (ActivityEntry, error) {
	var e ActivityEntry
	var before, after, undo *string
	if err := sc.Scan(&e.ID, &e.TS, &e.ThreadID, &e.Tool, &e.Target, &before, &after, &undo, &e.UndoneAt, &e.ThreadKind, &e.Category); err != nil {
		return e, err
	}
	var err error
	if e.Before, err = snippet(before); err != nil {
		return e, err
	}
	if e.After, err = snippet(after); err != nil {
		return e, err
	}
	e.Source = sourceWrite
	e.Undoable = undo != nil && *undo != "" && e.UndoneAt == nil
	return e, nil
}

func getWrite(ctx context.Context, q db.Querier, id int64) (*ActivityEntry, error) {
	e, err := scanWrite(q.QueryRowContext(ctx,
		"SELECT w.id, w.ts, w.thread_id, w.tool, w.target, w.before_json, w.after_json, w.undo_json, w.undone_at, t.kind, "+categorySQL+
			" FROM agent_writes w LEFT JOIN threads t ON t.id = w.thread_id WHERE w.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// timeline is timeline: mailbox auto-actions and agent writes merged newest first; it returns the entries from
// offset on, limit of them, and the offset of the next page (0 when there is none).
func timeline(ctx context.Context, q db.Querier, category string, limit, offset int) ([]ActivityEntry, int, error) {
	want := offset + limit + 1
	var entries []ActivityEntry
	if category == "" || category == categoryMail {
		actions, err := live.List(ctx, q, "", "", want, 0)
		if err != nil {
			return nil, 0, err
		}
		for _, a := range actions {
			entries = append(entries, mailEntry(a))
		}
	}
	if category != categoryMail {
		rows, err := q.QueryContext(ctx,
			"SELECT "+writeColumns+" FROM (SELECT w.*, t.kind AS thread_kind, "+categorySQL+" AS category FROM agent_writes w "+
				"LEFT JOIN threads t ON t.id = w.thread_id) WHERE (? = '' OR category = ?) ORDER BY ts DESC, id DESC LIMIT ?",
			category, category, want)
		if err != nil {
			return nil, 0, err
		}
		written, err := db.Collect(rows, func(r *sql.Rows) (ActivityEntry, error) { return scanWrite(r) })
		if err != nil {
			return nil, 0, err
		}
		entries = append(entries, written...)
	}
	slices.SortStableFunc(entries, func(a, b ActivityEntry) int {
		switch {
		case a.TS != b.TS:
			if a.TS > b.TS {
				return -1
			}
			return 1
		case a.ID > b.ID:
			return -1
		case a.ID < b.ID:
			return 1
		}
		return 0
	})
	next := 0
	if len(entries) > offset+limit {
		next = offset + limit
	}
	if offset > len(entries) {
		offset = len(entries)
	}
	end := min(offset+limit, len(entries))
	shown := entries[offset:end]
	if shown == nil {
		shown = []ActivityEntry{}
	}
	return shown, next, nil
}

func (s *service) activityPage(w http.ResponseWriter, r *http.Request) {
	category := web.QueryValue(r.URL.Query(), "category")
	offset := int64(0)
	if r.URL.Query().Has("offset") {
		raw := web.QueryValue(r.URL.Query(), "offset")
		n, ok := py.LaxInt64(raw)
		if !ok {
			web.Unprocessable(w, web.BadInt("query", "offset", raw))
			return
		}
		offset = n
	}
	if category != "" && !slices.Contains(categories, category) {
		s.fail(w, r, http.StatusUnprocessableEntity, "unknown category")
		return
	}
	offset = max(offset, 0)
	entries, next, err := timeline(r.Context(), s.deps.DB.Read, category, pageSize, int(offset))
	check(err)
	data := activityPage{Entries: entries, Category: category, Categories: categories, NextOffset: next, Offset: int(offset)}
	if web.IsPartial(r) && offset != 0 {
		s.fragment(w, r, http.StatusOK, "_activity_rows.gohtml", data, nil)
		return
	}
	s.page(w, r, http.StatusOK, "activity", "activity.gohtml", data, nil)
}

// undone answers an undo: the activity page again for a plain post, the row's new state for htmx.
func (s *service) undone(w http.ResponseWriter, r *http.Request, entry ActivityEntry, notice *tpl.Toast) {
	if !web.IsPartial(r) {
		http.Redirect(w, r, "/activity", http.StatusSeeOther)
		return
	}
	s.fragment(w, r, http.StatusOK, "_activity_row.gohtml", activityRowData{E: entry}, notice)
}

func (s *service) undoMail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "action_id")
	if !ok {
		return
	}
	ctx := detached(r)
	known, err := live.Get(ctx, s.deps.DB.Write, id)
	check(err)
	if known == nil {
		s.fail(w, r, http.StatusNotFound, "unknown action")
		return
	}
	var notice *tpl.Toast
	_, err = s.live.Undo(ctx, id)
	var refused live.UndoError
	switch {
	case errors.As(err, &refused):
		if !web.IsPartial(r) {
			s.fail(w, r, http.StatusConflict, err.Error())
			return
		}
		notice = toast("error", "Not undone", err.Error())
	case err != nil:
		panic(err)
	default:
		notice = toast("success", "Undone")
	}
	found, err := live.Get(ctx, s.deps.DB.Write, id)
	check(err)
	s.undone(w, r, mailEntry(*found), notice)
}

func (s *service) undoWrite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "write_id")
	if !ok {
		return
	}
	ctx := detached(r)
	known, err := getWrite(ctx, s.deps.DB.Write, id)
	check(err)
	if known == nil {
		s.fail(w, r, http.StatusNotFound, "unknown write")
		return
	}
	var notice *tpl.Toast
	_, err = s.writes.Undo(ctx, s.deps.DB.Write, id, s.now)
	var refused writes.UndoError
	switch {
	case errors.As(err, &refused):
		if !web.IsPartial(r) {
			s.fail(w, r, http.StatusConflict, err.Error())
			return
		}
		notice = toast("error", "Not undone", err.Error())
	case err != nil:
		panic(err)
	default:
		notice = toast("success", "Undone", fmt.Sprintf("Change %d was reverted.", id))
	}
	found, err := getWrite(ctx, s.deps.DB.Write, id)
	check(err)
	s.undone(w, r, *found, notice)
}
