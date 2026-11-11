package reviewpages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
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

func effectFilter(category string, limit int) ledger.Filter {
	f := ledger.Filter{Origins: directOrigins, Limit: limit}
	switch category {
	case categoryWiki:
		f.OperationLike = []string{"notion%"}
	case categoryKnowledge:
		f.OperationLike = []string{"kb%"}
	case categoryMedia:
		f.OperationLike = []string{"media%"}
	case categoryAssistant:
		f.OperationNotLike = []string{"notion%", "kb%", "media%"}
	}
	return f
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

func timeline(ctx context.Context, q db.Querier, store *ledger.Store, liveSvc *live.Live, category string, limit, offset int) ([]ActivityEntry, int, error) {
	want := offset + limit + 1
	var entries []ActivityEntry
	if category == "" || category == categoryMail {
		actions, err := liveSvc.List(ctx, "", "", want, 0)
		if err != nil {
			return nil, 0, err
		}
		for _, a := range actions {
			entries = append(entries, mailEntry(a))
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
		autos, err := ledger.AutoResolutionsSince(ctx, q, "phone", want)
		if err != nil {
			return nil, 0, err
		}
		for _, r := range autos {
			phone = append(phone, ActivityEntry{
				ID: r.ID, TS: r.TS, Source: "phone", Category: categoryMail,
				Phone: &PhoneActivity{Actor: r.Actor, Kind: r.Operation, Status: r.Outcome, Target: fmt.Sprintf("mail %d", r.EffectID)},
			})
		}
		audits, err := ledger.ProposalAuditsSince(ctx, q, "phone", want)
		if err != nil {
			return nil, 0, err
		}
		for _, a := range audits {
			phone = append(phone, ActivityEntry{
				ID: a.ID, TS: a.TS, Source: "phone", Category: categoryMail,
				Phone: &PhoneActivity{Actor: a.Actor, Kind: "proposal", Status: a.Outcome, Target: fmt.Sprintf("proposal %d", a.ProposalID)},
			})
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
	if category != categoryMail && category != categorySettings && store != nil {
		effects, err := store.List(ctx, effectFilter(category, want))
		if err != nil {
			return nil, 0, err
		}
		for _, effect := range effects {
			if effect.Direction != "forward" {
				continue
			}
			entries = append(entries, effectEntry(effect))
		}
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
	entries, next, err := timeline(r.Context(), s.deps.DB.Read, s.ledger, s.live, category, pageSize, int(offset))
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
	known, err := s.live.Get(ctx, id)
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
	found, err := s.live.Get(ctx, id)
	check(err)
	s.undone(w, r, mailEntry(*found), notice)
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
	found, err := s.live.Get(ctx, id)
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown action")
		return
	}
	s.undone(w, r, mailEntry(*found), notice)
}

func effectEntry(e ledger.Effect) ActivityEntry {
	before, _ := snippet(rawTextPtr(e.Before))
	after, _ := snippet(rawTextPtr(e.After))
	var errorText *string
	if e.Error != "" {
		errorText = &e.Error
	}
	return ActivityEntry{
		Source: "effect", ID: e.ID, Category: writeCategory(e.Operation), TS: e.CreatedAt,
		Write: &WriteActivity{
			Tool: e.Operation, Target: e.Target, ThreadID: e.ThreadID, UndoneAt: e.UndoneAt,
			Before: before, After: after, State: string(e.State), Error: errorText, Undoable: e.Undoable,
			Resolved: e.Resolution != nil,
		},
	}
}

func rawTextPtr(raw json.RawMessage) *string {
	if len(raw) == 0 {
		return nil
	}
	text := string(raw)
	return &text
}

func (s *service) undoEffect(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "effect_id")
	if !ok {
		return
	}
	ctx := detached(r)
	var missing *ledger.NotFound
	if _, err := s.ledger.Get(ctx, id); errors.As(err, &missing) {
		s.fail(w, r, http.StatusNotFound, "unknown effect")
		return
	} else if err != nil {
		panic(err)
	}
	var notice *tpl.Toast
	_, err := s.ledger.Undo(ctx, id, ledger.Resolver{Actor: "operator", Source: "ui"})
	var refused ledger.Refused
	var conflict *ledger.Conflict
	var settled *ledger.SettledMeanwhile
	switch {
	case errors.As(err, &refused) || errors.As(err, &conflict) || errors.As(err, &settled):
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
	found, err := s.ledger.Get(ctx, id)
	check(err)
	s.undone(w, r, effectEntry(found), notice)
}

func (s *service) resolveEffect(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "effect_id")
	if !ok {
		return
	}
	ctx := detached(r)
	outcome, _ := web.Value(web.FormValues(r), "outcome")
	var notice *tpl.Toast
	_, err := s.ledger.Resolve(ctx, id, ledger.Resolution{Outcome: outcome}, ledger.Resolver{Actor: "operator", Source: "ui"}, nil)
	var refused ledger.Refused
	var missing *ledger.NotFound
	switch {
	case errors.As(err, &missing):
		s.fail(w, r, http.StatusNotFound, "unknown effect")
		return
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
	found, err := s.ledger.Get(ctx, id)
	check(err)
	s.undone(w, r, effectEntry(found), notice)
}

func writeUndoDetail(err error) string {
	if strings.Contains(err.Error(), "unknown undo kind") {
		return "Not undone: atrium has no way to undo this change."
	}
	return err.Error()
}
