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

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
	"github.com/jeiang/atrium/internal/writes"
)

const (
	categoryAssistant = "assistant"
	categoryWiki      = "wiki"
	categoryKnowledge = "knowledge"
	categoryMedia     = "media"
	categorySettings  = "settings"
	pageSize          = 50
	snippetLimit      = 800
)

var categories = []string{categoryMail, categoryAssistant, categoryWiki, categoryKnowledge, categoryMedia, categorySettings}

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
	{"kb", categoryKnowledge},
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

func writeEntry(w writes.Write, threadKind *string) (ActivityEntry, error) {
	var e ActivityEntry
	e.ID, e.TS = w.ID, w.TS
	beforeText, err := snippet(w.Before)
	if err != nil {
		return e, err
	}
	afterText, err := snippet(w.After)
	if err != nil {
		return e, err
	}
	e.Source = sourceWrite
	e.Category = writeCategory(w.Tool)
	e.Write = &WriteActivity{
		Tool: w.Tool, Target: w.Target, ThreadID: w.ThreadID, ThreadKind: threadKind, UndoneAt: w.UndoneAt,
		Before: beforeText, After: afterText, State: w.State, Error: w.Error,
		Undoable: w.Undo != nil && *w.Undo != "" && w.UndoneAt == nil && w.State != "unknown",
	}
	return e, nil
}

func getWrite(ctx context.Context, q db.Querier, id int64) (*ActivityEntry, error) {
	w, err := writes.ByID(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, nil
	}
	var kind *string
	if w.ThreadID != nil {
		found, err := threads.KindOf(ctx, q, *w.ThreadID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			text := string(found)
			kind = &text
		}
	}
	e, err := writeEntry(*w, kind)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func settingsEntries(ctx context.Context, q db.Querier, limit int) ([]ActivityEntry, error) {
	rows, err := audit.Recent(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ActivityEntry, 0, len(rows))
	for _, row := range rows {
		e := ActivityEntry{ID: row.ID, TS: row.TS, Source: sourceSettings, Category: categorySettings}
		e.Settings = &SettingsActivity{Tool: row.Key, Actor: row.Actor, Origin: row.Source, Before: deref(row.Old), After: deref(row.New)}
		out = append(out, e)
	}
	return out, nil
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
	if category == "" || category == categoryMail {
		found, err := writes.ResolutionsSince(ctx, q, want)
		if err != nil {
			return nil, 0, err
		}
		phone := make([]ActivityEntry, 0, len(found))
		for _, r := range found {
			var e ActivityEntry
			target := fmt.Sprintf("%s %d", r.Kind, r.RowID)
			if r.ProposalID != nil {
				target = fmt.Sprintf("proposal %d", *r.ProposalID)
			}
			e.ID, e.TS = r.ID, r.TS
			e.Source, e.Category = "phone", categoryMail
			e.Phone = &PhoneActivity{Actor: r.Actor, Kind: r.Kind, Status: r.Outcome, Target: target}
			phone = append(phone, e)
		}
		entries = append(entries, phone...)
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
		found, err := writes.List(ctx, q, where, args, want)
		if err != nil {
			return nil, 0, err
		}
		ids := make([]int64, 0, len(found))
		for _, w := range found {
			if w.ThreadID != nil {
				ids = append(ids, *w.ThreadID)
			}
		}
		kinds, err := threads.KindsOf(ctx, q, ids)
		if err != nil {
			return nil, 0, err
		}
		written := make([]ActivityEntry, 0, len(found))
		for _, w := range found {
			var kind *string
			if w.ThreadID != nil {
				if k, ok := kinds[*w.ThreadID]; ok {
					text := string(k)
					kind = &text
				}
			}
			entry, err := writeEntry(w, kind)
			if err != nil {
				return nil, 0, err
			}
			written = append(written, entry)
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
	if receiptView(r) {
		s.fragment(w, r, http.StatusOK, "_receipt.gohtml", activityRowData{E: entry}, notice)
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
