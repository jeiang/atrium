package reviewpages

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/web"
)

var appResolver = proposals.Resolver{Actor: "operator", Source: "ui"}

type apiApproval struct {
	ID            int64    `json:"id"`
	Kind          string   `json:"kind"`
	KindLabel     string   `json:"kind_label"`
	Module        string   `json:"module"`
	Approval      bool     `json:"approval"`
	Status        string   `json:"status"`
	Account       *string  `json:"account"`
	Title         string   `json:"title"`
	ThreadID      *int64   `json:"thread_id"`
	RunID         *int64   `json:"run_id"`
	CreatedAt     float64  `json:"created_at"`
	DecidedAt     *float64 `json:"decided_at"`
	AppliedAt     *float64 `json:"applied_at"`
	UndoneAt      *float64 `json:"undone_at"`
	Undoable      bool     `json:"undoable"`
	Blocked       bool     `json:"blocked"`
	BlockedEffect bool     `json:"blocked_effect"`
	BlockedUndo   bool     `json:"blocked_undo"`
	Args          []Arg    `json:"args"`
	Preview       any      `json:"preview"`
	Result        any      `json:"result"`
}

type apiDraft struct {
	ID                   int64    `json:"id"`
	ThreadID             *int64   `json:"thread_id"`
	Account              string   `json:"account"`
	To                   string   `json:"to"`
	Cc                   string   `json:"cc"`
	Subject              string   `json:"subject"`
	Body                 string   `json:"body"`
	InReplyToMessageID   *int64   `json:"in_reply_to_message_id"`
	Status               string   `json:"status"`
	Revision             int64    `json:"revision"`
	CreatedAt            float64  `json:"created_at"`
	UpdatedAt            float64  `json:"updated_at"`
	SentAt               *float64 `json:"sent_at"`
	SMTPResult           *string  `json:"smtp_result"`
	UnexpectedRecipients []string `json:"unexpected_recipients"`
	AccountConfigured    bool     `json:"account_configured"`
}

type apiUndoEntry struct {
	ID       string   `json:"id"`
	Source   string   `json:"source"`
	Tool     string   `json:"tool"`
	Target   string   `json:"target"`
	Before   string   `json:"before"`
	After    string   `json:"after"`
	TS       float64  `json:"ts"`
	ThreadID *int64   `json:"thread_id"`
	RunID    *int64   `json:"run_id"`
	State    string   `json:"state"`
	Error    *string  `json:"error"`
	UndoneAt *float64 `json:"undone_at"`
	Undoable bool     `json:"undoable"`
}

type undoCursor struct {
	TS     float64 `json:"t"`
	Source int     `json:"s"`
	ID     int64   `json:"i"`
}

const (
	undoWrite    = 0
	undoProposal = 1
)

func (s *service) apiRoutes(d *web.Deps) {
	d.API(http.MethodGet, "/pending", s.apiPending)
	d.API(http.MethodPost, "/approvals/{proposal_id}/approve", web.Idempotent(s.apiApprove))
	d.API(http.MethodPost, "/approvals/{proposal_id}/deny", s.apiDeny)
	d.API(http.MethodPost, "/approvals/{proposal_id}/resolve", s.apiResolve)
	d.API(http.MethodGet, "/undo", s.apiUndoFeed)
	d.API(http.MethodPost, "/undo/{entry_id}", s.apiUndo)
	d.API(http.MethodGet, "/effects", s.apiEffects)
	d.API(http.MethodGet, "/effects/{effect_id}", s.apiEffect)
	d.API(http.MethodPost, "/effects/{effect_id}/resolve", s.apiEffectResolve)
	d.API(http.MethodPost, "/effects/{effect_id}/undo", s.apiEffectUndo)
	d.API(http.MethodGet, "/drafts/{draft_id}", s.apiDraft)
	d.API(http.MethodPatch, "/drafts/{draft_id}", s.apiDraftUpdate)
	d.API(http.MethodPost, "/drafts/{draft_id}/send", web.Idempotent(s.apiDraftSend))
	d.API(http.MethodPost, "/drafts/{draft_id}/discard", s.apiDraftDiscard)
	d.API(http.MethodPost, "/drafts/{draft_id}/resolve", s.apiDraftResolve)
}

func payloadRefs(raw json.RawMessage) (thread, run *int64) {
	var p approval.Payload
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil {
		return nil, nil
	}
	if p.ThreadID != 0 {
		thread = &p.ThreadID
	}
	if p.RunID != 0 {
		run = &p.RunID
	}
	return thread, run
}

func (s *service) approvalOf(p *proposals.Proposal) apiApproval {
	kind, _ := s.registry.Kind(p.Kind)
	thread, run := payloadRefs(p.Payload)
	out := apiApproval{
		ID: p.ID, Kind: p.Kind, KindLabel: kind.Label, Module: kind.Module, Approval: kind.Approval, Status: p.Status,
		Account: p.Account, Title: p.Title, ThreadID: thread, RunID: run, CreatedAt: p.CreatedTS, DecidedAt: p.DecidedTS,
		AppliedAt: p.AppliedTS, UndoneAt: p.UndoneTS, Undoable: p.Undoable, Blocked: p.Blocked, BlockedEffect: p.BlockedEffect, BlockedUndo: p.BlockedUndo,
		Args: []Arg{}, Preview: plainJSON(p.Preview), Result: plainJSON(p.Result),
	}
	if kind.Approval {
		out.Args = approvalArgs(p.Preview, p.Kind != permission.Kind)
	}
	return out
}

func (s *service) approvalsOf(ctx context.Context, list []*proposals.Proposal) []apiApproval {
	out := make([]apiApproval, len(list))
	for i, p := range list {
		full, err := proposals.Get(ctx, s.deps.DB.Read, p.ID)
		check(err)
		if full == nil {
			full = p
		}
		out[i] = s.approvalOf(full)
	}
	return out
}

func (s *service) draftsOf(ctx context.Context, rows []drafts.Row) []apiDraft {
	unexpected, err := s.drafts.UnexpectedRecipientsBatch(ctx, rows)
	check(err)
	configured := accountConfigured(ctx)
	out := make([]apiDraft, len(rows))
	for i, row := range rows {
		recipients := unexpected[i]
		if recipients == nil {
			recipients = []string{}
		}
		out[i] = apiDraft{
			ID: row.ID, ThreadID: row.ThreadID, Account: row.Account, To: row.ToAddrs, Cc: row.CcAddrs, Subject: row.Subject,
			Body: row.Body, InReplyToMessageID: row.InReplyToMessageID, Status: string(row.Status), Revision: row.Revision,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, SentAt: row.SentAt, SMTPResult: row.SMTPResult,
			UnexpectedRecipients: recipients, AccountConfigured: configured[row.Account],
		}
	}
	return out
}

func (s *service) apiPending(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	found := s.needs(ctx)
	unknown := 0
	for _, row := range found.drafts {
		if row.Status == drafts.Unknown {
			unknown++
		}
	}
	web.JSON(w, http.StatusOK, map[string]any{
		"approvals": s.approvalsOf(ctx, found.pending),
		"blocked":   s.approvalsOf(ctx, found.blocked),
		"drafts":    s.draftsOf(ctx, found.drafts),
		"counts": map[string]int{
			"approvals": len(found.pending), "blocked": len(found.blocked),
			"drafts": len(found.drafts) - unknown, "unknown": unknown,
		},
	})
}

func (s *service) apiProposal(w http.ResponseWriter, r *http.Request) (*proposals.Proposal, bool) {
	id, ok := web.PathID(w, r, "proposal_id")
	if !ok {
		return nil, false
	}
	found, err := proposals.Get(r.Context(), s.deps.DB.Read, id)
	check(err)
	if found == nil {
		web.APIError(w, http.StatusNotFound, "not_found", "Unknown approval.")
		return nil, false
	}
	return found, true
}

func (s *service) currentApproval(ctx context.Context, id int64) apiApproval {
	found, err := proposals.Get(ctx, s.deps.DB.Read, id)
	check(err)
	return s.approvalOf(found)
}

func (s *service) apiApprove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Selection json.RawMessage `json:"selection"`
	}
	if !web.DecodeOptionalJSON(w, r, &in) {
		return
	}
	found, ok := s.apiProposal(w, r)
	if !ok {
		return
	}
	var selection any
	if len(in.Selection) > 0 && string(in.Selection) != "null" {
		picked, err := jsonx.Loads(in.Selection)
		if _, isMap := picked.(map[string]any); err != nil || !isMap {
			web.APIError(w, http.StatusUnprocessableEntity, "bad_selection", "selection must be an object.")
			return
		}
		selection = picked
	}
	ctx := detached(r)
	done, err := s.pipeline.ApproveAndApply(ctx, found.ID, selection, appResolver)
	var badSelection proposals.SelectionError
	var refused proposals.ProposalError
	switch {
	case errors.As(err, &badSelection):
		web.APIError(w, http.StatusUnprocessableEntity, "bad_selection", err.Error())
		return
	case errors.As(err, &refused):
		web.APIError(w, http.StatusConflict, "not_approved", err.Error())
		return
	}
	check(err)
	out := map[string]any{"approval": s.approvalOf(done)}
	if done.Status == proposals.Applied {
		out["outcome"] = "applied"
		out["summary"] = resultString(done.Result, "summary")
	} else {
		out["outcome"] = "failed"
		out["error"] = resultString(done.Result, "error")
	}
	web.JSON(w, http.StatusOK, out)
}

func (s *service) apiDeny(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Forever bool `json:"forever"`
	}
	if !web.DecodeOptionalJSON(w, r, &in) {
		return
	}
	found, ok := s.apiProposal(w, r)
	if !ok {
		return
	}
	status := proposals.Dismissed
	if in.Forever {
		status = proposals.Suppressed
	}
	ctx := detached(r)
	err := s.pipeline.Dismiss(ctx, found.ID, status, appResolver)
	var refused proposals.ProposalError
	if errors.As(err, &refused) {
		web.APIError(w, http.StatusConflict, "not_dismissed", err.Error())
		return
	}
	check(err)
	web.JSON(w, http.StatusOK, map[string]any{"approval": s.currentApproval(ctx, found.ID)})
}

func (s *service) apiResolve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Outcome string `json:"outcome"`
	}
	if !web.DecodeOptionalJSON(w, r, &in) {
		return
	}
	found, ok := s.apiProposal(w, r)
	if !ok {
		return
	}
	ctx := detached(r)
	_, err := s.pipeline.Resolve(ctx, found.ID, in.Outcome, appResolver)
	var refused proposals.ProposalError
	if errors.As(err, &refused) {
		web.APIError(w, http.StatusConflict, "not_resolved", err.Error())
		return
	}
	check(err)
	web.JSON(w, http.StatusOK, map[string]any{"approval": s.currentApproval(ctx, found.ID)})
}

func undoEffectEntry(e ledger.Effect) (apiUndoEntry, error) {
	var entry apiUndoEntry
	entry.TS, entry.ThreadID, entry.RunID, entry.Tool, entry.Target = e.CreatedAt, e.ThreadID, e.RunID, e.Operation, e.Target
	entry.UndoneAt, entry.State = e.UndoneAt, string(e.State)
	if e.Error != "" {
		text := e.Error
		entry.Error = &text
	}
	var err error
	if entry.Before, err = snippet(rawTextPtr(e.Before)); err != nil {
		return entry, err
	}
	if entry.After, err = snippet(rawTextPtr(e.After)); err != nil {
		return entry, err
	}
	entry.ID = "write:" + strconv.FormatInt(e.ID, 10)
	entry.Source = sourceWrite
	entry.Undoable = e.Undoable
	return entry, nil
}

func undoProposalEntry(p *proposals.Proposal) apiUndoEntry {
	thread, run := payloadRefs(p.Payload)
	ts := p.CreatedTS
	if p.AppliedTS != nil {
		ts = *p.AppliedTS
	}
	return apiUndoEntry{
		ID: "proposal:" + strconv.FormatInt(p.ID, 10), Source: "proposal", Tool: p.Kind, Target: p.Title, TS: ts,
		ThreadID: thread, RunID: run, State: p.Status, UndoneAt: p.UndoneTS, Undoable: p.Undoable && !p.BlockedUndo,
	}
}

func optionalID(w http.ResponseWriter, r *http.Request, name string) (*int64, bool) {
	raw := web.QueryValue(r.URL.Query(), name)
	if raw == "" {
		return nil, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 {
		web.APIError(w, http.StatusUnprocessableEntity, "invalid", name+" must be a positive integer.", web.BadInt("query", name, raw))
		return nil, false
	}
	return &n, true
}

func (s *service) apiUndoFeed(w http.ResponseWriter, r *http.Request) {
	thread, ok := optionalID(w, r, "thread_id")
	if !ok {
		return
	}
	run, ok := optionalID(w, r, "run_id")
	if !ok {
		return
	}
	var cursor undoCursor
	limit, after, ok := web.Paging(w, r, &cursor)
	if !ok {
		return
	}
	ctx := r.Context()
	var propWhere []string
	var propArgs []any
	if thread != nil {
		propWhere = append(propWhere, "json_extract(payload, '$.thread_id') = ?")
		propArgs = append(propArgs, *thread)
	}
	if run != nil {
		propWhere = append(propWhere, "json_extract(payload, '$.run_id') = ?")
		propArgs = append(propArgs, *run)
	}
	if after {
		propWhere = append(propWhere, "(COALESCE(applied_ts, created_ts) < ? OR (COALESCE(applied_ts, created_ts) = ? AND ? = 1 AND id < ?))")
		propArgs = append(propArgs, cursor.TS, cursor.TS, cursor.Source, cursor.ID)
	}
	filter := ledger.Filter{Origins: directOrigins, Limit: limit + 1, ThreadID: thread, RunID: run}
	if after {
		filter.BeforeTS = cursor.TS
		if cursor.Source == undoProposal {
			filter.BeforeID = math.MaxInt64
		} else {
			filter.BeforeID = cursor.ID
		}
	}
	found, err := s.ledger.List(ctx, filter)
	check(err)
	written := make([]apiUndoEntry, 0, len(found))
	for _, e := range found {
		if !e.Undoable {
			continue
		}
		entry, err := undoEffectEntry(e)
		check(err)
		written = append(written, entry)
	}
	applied, err := proposals.ListUndoable(ctx, s.deps.DB.Read, strings.Join(propWhere, " AND "), propArgs, limit+1)
	check(err)
	keys := make([]undoCursor, 0, len(written)+len(applied))
	entries := make([]apiUndoEntry, 0, len(written)+len(applied))
	i, j := 0, 0
	for len(entries) < limit+1 && (i < len(written) || j < len(applied)) {
		var next *apiUndoEntry
		if j < len(applied) {
			p := undoProposalEntry(applied[j])
			next = &p
		}
		if next != nil && (i >= len(written) || next.TS >= written[i].TS) {
			entries = append(entries, *next)
			keys = append(keys, undoCursor{TS: next.TS, Source: undoProposal, ID: applied[j].ID})
			j++
			continue
		}
		entries = append(entries, written[i])
		id, _ := strconv.ParseInt(strings.TrimPrefix(written[i].ID, "write:"), 10, 64)
		keys = append(keys, undoCursor{TS: written[i].TS, Source: undoWrite, ID: id})
		i++
	}
	out := web.List[apiUndoEntry]{Items: entries}
	if len(entries) > limit {
		out.Items = entries[:limit]
		out.NextCursor = web.EncodeCursor(keys[limit-1])
	}
	web.JSON(w, http.StatusOK, out)
}

func (s *service) undoEffectEntry(ctx context.Context, id int64) (*apiUndoEntry, error) {
	e, err := s.ledger.Get(ctx, id)
	var missing *ledger.NotFound
	if errors.As(err, &missing) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entry, err := undoEffectEntry(e)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

func (s *service) apiUndo(w http.ResponseWriter, r *http.Request) {
	source, raw, _ := strings.Cut(web.PathParam(r, "entry_id"), ":")
	id, perr := strconv.ParseInt(raw, 10, 64)
	if perr != nil || id < 1 {
		web.APIError(w, http.StatusNotFound, "not_found", "Unknown undo entry.")
		return
	}
	ctx := detached(r)
	switch source {
	case "write":
		known, err := s.undoEffectEntry(ctx, id)
		check(err)
		if known == nil {
			web.APIError(w, http.StatusNotFound, "not_found", "Unknown undo entry.")
			return
		}
		_, err = s.ledger.Undo(ctx, id, ledger.Resolver{Actor: "operator", Source: "ui"})
		var refused ledger.Refused
		var conflict *ledger.Conflict
		var settled *ledger.SettledMeanwhile
		if errors.As(err, &refused) || errors.As(err, &conflict) || errors.As(err, &settled) {
			web.APIError(w, http.StatusConflict, "not_undone", err.Error())
			return
		}
		check(err)
		entry, err := s.undoEffectEntry(ctx, id)
		check(err)
		web.JSON(w, http.StatusOK, map[string]any{"entry": entry, "undone": true})
	case "proposal":
		known, err := proposals.Get(ctx, s.deps.DB.Read, id)
		check(err)
		if known == nil {
			web.APIError(w, http.StatusNotFound, "not_found", "Unknown undo entry.")
			return
		}
		outcome, err := s.pipeline.Undo(ctx, id, proposals.Resolver{Actor: "operator", Source: "ui"})
		var refused proposals.ProposalError
		if errors.As(err, &refused) {
			web.APIError(w, http.StatusConflict, "not_undone", err.Error())
			return
		}
		check(err)
		after, err := proposals.Get(ctx, s.deps.DB.Read, id)
		check(err)
		out := map[string]any{"entry": undoProposalEntry(after), "undone": outcome.Undone}
		if !outcome.Undone {
			for _, o := range outcome.Outcomes {
				if o["ok"] != true {
					out["error"], _ = o["error"].(string)
					break
				}
			}
		}
		web.JSON(w, http.StatusOK, out)
	default:
		web.APIError(w, http.StatusNotFound, "not_found", "Unknown undo entry.")
	}
}

func (s *service) apiEffects(w http.ResponseWriter, r *http.Request) {
	f := ledger.Filter{
		Origin:      web.QueryValue(r.URL.Query(), "origin"),
		Operation:   web.QueryValue(r.URL.Query(), "operation"),
		BlockedOnly: web.QueryValue(r.URL.Query(), "blocked") == "true",
	}
	for name, dest := range map[string]**int64{"proposal_id": &f.ProposalID, "thread_id": &f.ThreadID, "run_id": &f.RunID} {
		id, ok := optionalID(w, r, name)
		if !ok {
			return
		}
		*dest = id
	}
	if raw := web.QueryValue(r.URL.Query(), "before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			web.APIError(w, http.StatusUnprocessableEntity, "invalid", "before must be a non-negative integer.", web.BadInt("query", "before", raw))
			return
		}
		f.BeforeID = n
	}
	if raw := web.QueryValue(r.URL.Query(), "limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			web.APIError(w, http.StatusUnprocessableEntity, "invalid", "limit must be a positive integer.", web.BadInt("query", "limit", raw))
			return
		}
		f.Limit = n
	}
	found, err := s.ledger.List(r.Context(), f)
	check(err)
	web.JSON(w, http.StatusOK, web.List[ledger.Effect]{Items: found})
}

func (s *service) apiEffect(w http.ResponseWriter, r *http.Request) {
	id, ok := web.PathID(w, r, "effect_id")
	if !ok {
		return
	}
	found, err := s.ledger.Get(r.Context(), id)
	var missing *ledger.NotFound
	if errors.As(err, &missing) {
		web.APIError(w, http.StatusNotFound, "not_found", "Unknown effect.")
		return
	}
	check(err)
	web.JSON(w, http.StatusOK, found)
}

func (s *service) apiEffectResolve(w http.ResponseWriter, r *http.Request) {
	id, ok := web.PathID(w, r, "effect_id")
	if !ok {
		return
	}
	var in struct {
		Outcome  string          `json:"outcome"`
		Evidence json.RawMessage `json:"evidence"`
	}
	if !web.DecodeOptionalJSON(w, r, &in) {
		return
	}
	ctx := detached(r)
	found, err := s.ledger.Resolve(ctx, id, ledger.Resolution{Outcome: in.Outcome, Evidence: in.Evidence}, ledger.Resolver{Actor: "operator", Source: "ui"}, nil)
	var refused ledger.Refused
	var missing *ledger.NotFound
	switch {
	case errors.As(err, &missing):
		web.APIError(w, http.StatusNotFound, "not_found", "Unknown effect.")
		return
	case errors.As(err, &refused):
		web.APIError(w, http.StatusConflict, "not_resolved", err.Error())
		return
	}
	check(err)
	web.JSON(w, http.StatusOK, map[string]any{"effect": found})
}

func (s *service) apiEffectUndo(w http.ResponseWriter, r *http.Request) {
	id, ok := web.PathID(w, r, "effect_id")
	if !ok {
		return
	}
	ctx := detached(r)
	effect, err := s.ledger.Undo(ctx, id, ledger.Resolver{Actor: "operator", Source: "ui"})
	var refused ledger.Refused
	var conflict *ledger.Conflict
	var settled *ledger.SettledMeanwhile
	var missing *ledger.NotFound
	switch {
	case errors.As(err, &missing):
		web.APIError(w, http.StatusNotFound, "not_found", "Unknown effect.")
		return
	case errors.As(err, &refused) || errors.As(err, &conflict) || errors.As(err, &settled):
		web.APIError(w, http.StatusConflict, "not_undone", err.Error())
		return
	}
	check(err)
	forward, err := s.ledger.Get(ctx, id)
	check(err)
	web.JSON(w, http.StatusOK, map[string]any{"effect": forward, "undo": effect, "undone": true})
}

func (s *service) apiDraftRow(w http.ResponseWriter, r *http.Request) (*drafts.Row, bool) {
	id, ok := web.PathID(w, r, "draft_id")
	if !ok {
		return nil, false
	}
	row, err := s.drafts.Get(r.Context(), id)
	check(err)
	if row == nil {
		web.APIError(w, http.StatusNotFound, "not_found", "Unknown draft.")
		return nil, false
	}
	return row, true
}

func (s *service) currentDraft(ctx context.Context, id int64) apiDraft {
	row, err := s.drafts.Get(ctx, id)
	check(err)
	return s.draftsOf(ctx, []drafts.Row{*row})[0]
}

func (s *service) apiDraft(w http.ResponseWriter, r *http.Request) {
	row, ok := s.apiDraftRow(w, r)
	if !ok {
		return
	}
	web.JSON(w, http.StatusOK, s.draftsOf(r.Context(), []drafts.Row{*row})[0])
}

func (s *service) apiDraftUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Account *string `json:"account"`
		To      *string `json:"to"`
		Cc      *string `json:"cc"`
		Subject *string `json:"subject"`
		Body    *string `json:"body"`
	}
	if !web.DecodeJSON(w, r, &in) {
		return
	}
	row, ok := s.apiDraftRow(w, r)
	if !ok {
		return
	}
	changes := map[string]any{}
	for name, v := range map[string]*string{"account": in.Account, "to": in.To, "cc": in.Cc, "subject": in.Subject, "body": in.Body} {
		if v != nil {
			changes[name] = *v
		}
	}
	ctx := detached(r)
	_, err := s.drafts.Update(ctx, row.ID, changes)
	var invalid drafts.Error
	var stale drafts.Stale
	switch {
	case errors.As(err, &stale):
		web.APIError(w, http.StatusConflict, "stale", err.Error())
		return
	case errors.As(err, &invalid):
		web.APIError(w, http.StatusUnprocessableEntity, "invalid_draft", err.Error())
		return
	}
	check(err)
	web.JSON(w, http.StatusOK, s.currentDraft(ctx, row.ID))
}

func filedCode(f drafts.Filed) string {
	switch f {
	case drafts.FiledCopy:
		return "copy"
	case drafts.FiledByServer:
		return "server"
	case drafts.FiledNoMailbox:
		return "no_mailbox"
	case drafts.FiledFailed:
		return "failed"
	}
	return "none"
}

func (s *service) apiDraftSend(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision *int64 `json:"revision"`
	}
	if !web.DecodeOptionalJSON(w, r, &in) {
		return
	}
	if in.Revision == nil {
		web.APIError(w, http.StatusUnprocessableEntity, "invalid", "revision is required.", web.Missing("body", "revision"))
		return
	}
	row, ok := s.apiDraftRow(w, r)
	if !ok {
		return
	}
	ctx := detached(r)
	result, err := s.drafts.Send(ctx, row.ID, *in.Revision)
	var unknown drafts.OutcomeUnknown
	switch {
	case errors.As(err, &unknown):
		web.APIError(w, http.StatusConflict, "send_outcome_unknown", err.Error())
		return
	case err != nil:
		current, gerr := s.drafts.Get(ctx, row.ID)
		check(gerr)
		if current != nil && current.Status == drafts.Draft && current.Revision != *in.Revision {
			web.APIError(w, http.StatusConflict, "stale", err.Error())
			return
		}
		web.APIError(w, http.StatusConflict, "not_sent", err.Error())
		return
	}
	refused := make([]string, len(result.Refused))
	for i, refusal := range result.Refused {
		refused[i] = refusal.Addr
	}
	out := map[string]any{"draft": s.currentDraft(ctx, row.ID), "filed": filedCode(result.Filed), "refused": refused}
	if result.FiledError != "" {
		out["filed_error"] = result.FiledError
	}
	web.JSON(w, http.StatusOK, out)
}

func (s *service) apiDraftDiscard(w http.ResponseWriter, r *http.Request) {
	row, ok := s.apiDraftRow(w, r)
	if !ok {
		return
	}
	ctx := detached(r)
	err := s.drafts.Discard(ctx, row.ID)
	var refused drafts.Error
	if errors.As(err, &refused) {
		web.APIError(w, http.StatusConflict, "not_discarded", err.Error())
		return
	}
	check(err)
	web.JSON(w, http.StatusOK, map[string]any{"draft": s.currentDraft(ctx, row.ID)})
}

func (s *service) apiDraftResolve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Outcome string `json:"outcome"`
	}
	if !web.DecodeJSON(w, r, &in) {
		return
	}
	var verdict drafts.Status
	switch in.Outcome {
	case "sent":
		verdict = drafts.Sent
	case "draft":
		verdict = drafts.Draft
	default:
		web.APIError(w, http.StatusUnprocessableEntity, "invalid", "outcome must be sent or draft.",
			web.FieldError{Type: "enum", Loc: []string{"body", "outcome"}, Msg: "Input should be 'sent' or 'draft'", Input: in.Outcome})
		return
	}
	row, ok := s.apiDraftRow(w, r)
	if !ok {
		return
	}
	ctx := detached(r)
	err := s.drafts.ResolveUnknown(ctx, row.ID, verdict)
	var refused drafts.Error
	if errors.As(err, &refused) {
		web.APIError(w, http.StatusConflict, "not_resolved", err.Error())
		return
	}
	check(err)
	web.JSON(w, http.StatusOK, map[string]any{"draft": s.currentDraft(ctx, row.ID)})
}
