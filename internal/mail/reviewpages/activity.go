package reviewpages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	categoryAssistant = "assistant"
	categoryWiki      = "wiki"
	categoryMedia     = "media"
	categorySettings  = "settings"
	pageSize          = 50
	snippetLimit      = 800
)

var categories = []string{categoryMail, categoryAssistant, categoryWiki, categoryMedia, categorySettings}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func mustJSONIndent(v any) string {
	b, _ := json.MarshalIndent(v, "", " ")
	return string(b)
}

var writeCategoryPrefixes = []struct{ Prefix, Category string }{
	{"notion", categoryWiki},
	{"media", categoryMedia},
}

func writeCategory(tool string) string {
	for _, prefix := range writeCategoryPrefixes {
		if strings.HasPrefix(tool, prefix.Prefix) {
			return prefix.Category
		}
	}
	return categoryAssistant
}

func writeCategoryWhere(category string) (string, []any) {
	if category == "" {
		return "", nil
	}
	var clauses []string
	var args []any
	for _, prefix := range writeCategoryPrefixes {
		if prefix.Category == category {
			clauses = append(clauses, "tool LIKE ?")
			args = append(args, prefix.Prefix+"%")
		}
	}
	if category == categoryAssistant {
		for _, prefix := range writeCategoryPrefixes {
			clauses = append(clauses, "tool NOT LIKE ?")
			args = append(args, prefix.Prefix+"%")
		}
	}
	return strings.Join(clauses, " AND "), args
}

const writeColumns = "id, ts, thread_id, tool, target, before_json, after_json, undo_json, undone_at, state, error, thread_kind"

func snippet(raw *string) (string, error) {
	if raw == nil || *raw == "" {
		return "", nil
	}
	var value any
	if err := json.Unmarshal([]byte(*raw), &value); err != nil {
		return "", err
	}
	text := []rune(mustJSONIndent(value))
	if len(text) <= snippetLimit {
		return string(text), nil
	}
	return string(text[:snippetLimit]) + "…", nil
}

func scanWrite(sc interface{ Scan(...any) error }) (ActivityEntry, error) {
	var e ActivityEntry
	var threadID *int64
	var tool, target string
	var before, after, undo *string
	var undoneAt *float64
	var threadKind *string
	var state string
	var errorText *string
	if err := sc.Scan(&e.ID, &e.TS, &threadID, &tool, &target, &before, &after, &undo, &undoneAt, &state, &errorText, &threadKind); err != nil {
		return e, err
	}
	beforeText, err := snippet(before)
	if err != nil {
		return e, err
	}
	afterText, err := snippet(after)
	if err != nil {
		return e, err
	}
	e.Source = sourceWrite
	e.Category = writeCategory(tool)
	e.Write = &WriteActivity{
		Tool: tool, Target: target, ThreadID: threadID, ThreadKind: threadKind, UndoneAt: undoneAt,
		Before: beforeText, After: afterText, State: state, Error: errorText,
		Undoable: undo != nil && *undo != "" && undoneAt == nil && state != "unknown",
	}
	return e, nil
}

func getWrite(ctx context.Context, q db.Querier, id int64) (*ActivityEntry, error) {
	e, err := scanWrite(q.QueryRowContext(ctx,
		"SELECT w.id, w.ts, w.thread_id, w.tool, w.target, w.before_json, w.after_json, w.undo_json, w.undone_at, w.state, w.error, t.kind "+
			"FROM agent_writes w LEFT JOIN threads t ON t.id = w.thread_id WHERE w.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func settingsEntries(ctx context.Context, q db.Querier, limit int) ([]ActivityEntry, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, ts, key, old_value, new_value, actor, source FROM settings_audit ORDER BY ts DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ActivityEntry, error) {
		e := ActivityEntry{Source: sourceSettings, Category: categorySettings}
		var key, actor, origin string
		var old, next *string
		if err := r.Scan(&e.ID, &e.TS, &key, &old, &next, &actor, &origin); err != nil {
			return e, err
		}
		e.Settings = &SettingsActivity{Tool: key, Actor: actor, Origin: origin, Before: deref(old), After: deref(next)}
		return e, nil
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func timeline(ctx context.Context, q db.Querier, category string, limit, offset int, now float64) ([]ActivityEntry, int, error) {
	want := offset + limit + 1
	var entries []ActivityEntry
	if category == "" || category == categoryMail {
		actions, err := live.List(ctx, q, "", "", want, 0)
		if err != nil {
			return nil, 0, err
		}
		for _, a := range actions {
			entries = append(entries, mailEntry(a, now))
		}
	}
	if category == "" || category == categorySettings {
		changes, err := settingsEntries(ctx, q, want)
		if err != nil {
			return nil, 0, err
		}
		entries = append(entries, changes...)
	}
	if category != categoryMail && category != categorySettings {
		where, args := writeCategoryWhere(category)
		query := "SELECT " + writeColumns + " FROM (SELECT w.*, t.kind AS thread_kind FROM agent_writes w " +
			"LEFT JOIN threads t ON t.id = w.thread_id)"
		if where != "" {
			query += " WHERE " + where
		}
		query += " ORDER BY ts DESC, id DESC LIMIT ?"
		args = append(args, want)
		rows, err := q.QueryContext(ctx, query, args...)
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
		n, ok := laxInt64(raw)
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
	entries, next, err := timeline(r.Context(), s.deps.DB.Read, category, pageSize, int(offset), s.now())
	check(err)
	data := activityPage{Entries: entries, Category: category, Categories: categories, NextOffset: next, Offset: int(offset)}
	if web.IsPartial(r) && offset != 0 {
		s.fragment(w, r, http.StatusOK, "_activity_rows.gohtml", data, nil)
		return
	}
	s.page(w, r, http.StatusOK, "activity", "activity.gohtml", data, nil)
}

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
	known, err := live.Get(ctx, s.deps.DB.Read, id)
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
	found, err := live.Get(ctx, s.deps.DB.Read, id)
	check(err)
	s.undone(w, r, mailEntry(*found, s.now()), notice)
}

func (s *service) resolveMail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "action_id")
	if !ok {
		return
	}
	ctx := detached(r)
	outcome, _ := web.Value(web.FormValues(r), "outcome")
	var notice *tpl.Toast
	_, err := s.live.Resolve(ctx, id, outcome, proposals.Resolver{Actor: "operator", Source: "ui"})
	var refused live.UndoError
	switch {
	case errors.As(err, &refused):
		if !web.IsPartial(r) {
			s.fail(w, r, http.StatusConflict, err.Error())
			return
		}
		notice = toast("error", "Not settled", err.Error())
	case err != nil:
		panic(err)
	default:
		notice = resolvedToast(outcome)
	}
	found, err := live.Get(ctx, s.deps.DB.Read, id)
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown action")
		return
	}
	s.undone(w, r, mailEntry(*found, s.now()), notice)
}

func (s *service) resolveWrite(w http.ResponseWriter, r *http.Request) {
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
	err = writes.Resolve(ctx, s.deps.DB, id, writes.Resolver{Actor: "operator", Source: "ui"}, s.now)
	var refused writes.UndoError
	switch {
	case errors.As(err, &refused):
		if !web.IsPartial(r) {
			s.fail(w, r, http.StatusConflict, err.Error())
			return
		}
		notice = toast("error", "Not resolved", err.Error())
	case err != nil:
		panic(err)
	default:
		notice = toast("success", "Resolved", fmt.Sprintf("Write %d was marked resolved.", id))
	}
	found, err := getWrite(ctx, s.deps.DB.Write, id)
	check(err)
	s.undone(w, r, *found, notice)
}

func writeUndoDetail(err error) string {
	if strings.Contains(err.Error(), "unknown undo kind") {
		return "Not undone: atrium has no way to undo this change."
	}
	return err.Error()
}

func (s *service) undoWrite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "write_id")
	if !ok {
		return
	}
	ctx := detached(r)
	known, err := getWrite(ctx, s.deps.DB.Read, id)
	check(err)
	if known == nil {
		s.fail(w, r, http.StatusNotFound, "unknown write")
		return
	}
	var notice *tpl.Toast
	_, err = s.writes.Undo(ctx, s.deps.DB, id, s.now)
	var refused writes.UndoError
	switch {
	case errors.As(err, &refused):
		detail := writeUndoDetail(err)
		if !web.IsPartial(r) {
			s.fail(w, r, http.StatusConflict, detail)
			return
		}
		notice = toast("error", "Not undone", detail)
	case err != nil:
		panic(err)
	default:
		notice = toast("success", "Undone", fmt.Sprintf("Change %d was reverted.", id))
	}
	found, err := getWrite(ctx, s.deps.DB.Read, id)
	check(err)
	s.undone(w, r, *found, notice)
}
