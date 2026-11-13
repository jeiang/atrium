package reviewpages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/module"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/web"
)

type apiApproval struct {
	ID            int64           `json:"id"`
	Kind          string          `json:"kind"`
	KindLabel     string          `json:"kind_label"`
	Module        string          `json:"module"`
	Approval      bool            `json:"approval"`
	Status        string          `json:"status"`
	Account       *string         `json:"account"`
	Title         string          `json:"title"`
	ThreadID      *int64          `json:"thread_id"`
	RunID         *int64          `json:"run_id"`
	CreatedAt     float64         `json:"created_at"`
	DecidedAt     *float64        `json:"decided_at"`
	AppliedAt     *float64        `json:"applied_at"`
	UndoneAt      *float64        `json:"undone_at"`
	Undoable      bool            `json:"undoable"`
	Blocked       bool            `json:"blocked"`
	BlockedEffect bool            `json:"blocked_effect"`
	BlockedUndo   bool            `json:"blocked_undo"`
	Args          []reviewapp.Arg `json:"args"`
	Preview       any             `json:"preview"`
	Result        any             `json:"result"`
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

func (s *service) apiRoutes(d *web.Deps) {
	d.API(http.MethodGet, "/pending", s.apiPending)
	d.API(http.MethodPost, "/approvals/{proposal_id}/approve", s.apiApprove)
	d.API(http.MethodPost, "/approvals/{proposal_id}/deny", s.apiDeny)
	d.API(http.MethodPost, "/approvals/{proposal_id}/resolve", s.apiResolve)
	d.API(http.MethodGet, "/undo", s.apiUndoFeed)
	d.API(http.MethodPost, "/undo/{entry_id}", s.apiUndo)
	d.API(http.MethodGet, "/effects", s.apiEffects)
	d.API(http.MethodGet, "/effects/{effect_id}", s.apiEffect)
	d.API(http.MethodPost, "/effects/{effect_id}/resolve", s.apiEffectResolve)
	d.API(http.MethodPost, "/effects/{effect_id}/undo", s.apiEffectUndo)
}

func (s *service) mailAPIRoutes(d *web.Deps) {
	d.API(http.MethodGet, "/drafts/{draft_id}", s.apiDraft)
	d.API(http.MethodPatch, "/drafts/{draft_id}", bindMail(http.HandlerFunc(s.apiDraftUpdate)).ServeHTTP)
	d.API(http.MethodPost, "/drafts/{draft_id}/send", bindMail(http.HandlerFunc(s.apiDraftSend)).ServeHTTP)
	d.API(http.MethodPost, "/drafts/{draft_id}/discard", bindMail(http.HandlerFunc(s.apiDraftDiscard)).ServeHTTP)
	d.API(http.MethodPost, "/drafts/{draft_id}/resolve", bindMail(http.HandlerFunc(s.apiDraftResolve)).ServeHTTP)
}

func approvalOf(c reviewapp.Card) apiApproval {
	return apiApproval{
		ID: c.ID, Kind: c.Kind, KindLabel: c.KindLabel, Module: c.Module, Approval: c.Approval, Status: c.Status,
		Account: c.Account, Title: c.Title, ThreadID: c.ThreadID, RunID: c.RunID, CreatedAt: c.CreatedAt, DecidedAt: c.DecidedAt,
		AppliedAt: c.AppliedAt, UndoneAt: c.UndoneAt, Undoable: c.Undoable, Blocked: c.Blocked, BlockedEffect: c.BlockedEffect, BlockedUndo: c.BlockedUndo,
		Args: c.Args, Preview: c.Preview, Result: c.Result,
	}
}

func approvalsOf(cards []reviewapp.Card) []apiApproval {
	out := make([]apiApproval, len(cards))
	for i, c := range cards {
		out[i] = approvalOf(c)
	}
	return out
}

func draftsOf(cards []reviewapp.DraftCard, configured map[string]bool) []apiDraft {
	out := make([]apiDraft, len(cards))
	for i, c := range cards {
		row := c.Row
		recipients := c.Unexpected
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

type pendingCounts struct {
	Approvals int `json:"approvals"`
	Blocked   int `json:"blocked"`
	Drafts    int `json:"drafts"`
	Unknown   int `json:"unknown"`
}

type pendingBody struct {
	Approvals []apiApproval `json:"approvals"`
	Blocked   []apiApproval `json:"blocked"`
	Counts    pendingCounts `json:"counts"`
	Drafts    []apiDraft    `json:"drafts"`
}

func (s *service) apiPending(w http.ResponseWriter, r *http.Request) {
	found, err := s.review.Pending(r.Context(), reviewapp.PendingInput{Refs: true})
	if err != nil {
		web.APIFail(w, err)
		return
	}
	data := found.Data
	unknown := 0
	for _, c := range data.Drafts {
		if c.Row.Status == drafts.Unknown {
			unknown++
		}
	}
	web.APIRead(w, r, found.Revision, pendingBody{
		Approvals: approvalsOf(data.Approvals),
		Blocked:   approvalsOf(data.Blocked),
		Drafts:    draftsOf(data.Drafts, data.Configured),
		Counts:    pendingCounts{Approvals: len(data.Approvals), Blocked: len(data.Blocked), Drafts: len(data.Drafts) - unknown, Unknown: unknown},
	})
}

func apiRefused(w http.ResponseWriter, err error, code, missing string, extra map[string]any) bool {
	if err == nil {
		return false
	}
	var e *application.Error
	if !errors.As(err, &e) {
		panic(err)
	}
	switch e.Code {
	case application.NotFound:
		web.APIError(w, http.StatusNotFound, "not_found", missing)
	case application.Invalid:
		fields := make([]web.FieldError, len(e.Fields))
		code = "invalid"
		for i, f := range e.Fields {
			fields[i] = web.FieldError{Type: f.Code, Loc: []string{"body", f.Name}, Msg: f.Message}
			if f.Name == "selection" {
				code = "bad_selection"
			}
		}
		web.APIError(w, http.StatusUnprocessableEntity, code, e.Message, fields...)
	case application.Refused, application.Conflict:
		if extra != nil {
			web.APIErrorWith(w, http.StatusConflict, code, e.Message, extra)
			return true
		}
		web.APIError(w, http.StatusConflict, code, e.Message)
	default:
		web.APIFail(w, err)
	}
	return true
}

func (s *service) currentApproval(ctx context.Context, id int64) apiApproval {
	found, err := s.review.Proposal(ctx, reviewapp.ProposalInput{ID: id})
	check(err)
	return approvalOf(found.Data)
}

func (s *service) apiApprove(w http.ResponseWriter, r *http.Request) {
	id, ok := web.PathID(w, r, "proposal_id")
	if !ok {
		return
	}
	var in struct {
		Selection json.RawMessage `json:"selection"`
	}
	if !web.DecodeOptionalJSON(w, r, &in) {
		return
	}
	input := reviewapp.ApproveInput{ProposalID: id}
	if len(in.Selection) > 0 && string(in.Selection) != "null" {
		picked, err := jsonx.Loads(in.Selection)
		if err != nil {
			panic(err)
		}
		input.SelectionValue = picked
	}
	done, err := s.review.Approve(detached(r), input)
	var extra map[string]any
	if done.Data.Proposal != nil {
		extra = map[string]any{"approval": approvalOf(s.review.CardOf(done.Data.Proposal)), "outcome": "failed"}
	}
	if apiRefused(w, err, "not_approved", "Unknown approval.", extra) {
		return
	}
	out := map[string]any{"approval": approvalOf(s.review.CardOf(done.Data.Proposal))}
	if done.Data.Applied {
		out["outcome"] = "applied"
		out["summary"] = resultString(done.Data.Proposal.Result, "summary")
	} else {
		out["outcome"] = "failed"
		out["error"] = resultString(done.Data.Proposal.Result, "error")
	}
	web.JSON(w, http.StatusOK, out)
}

func (s *service) apiDeny(w http.ResponseWriter, r *http.Request) {
	id, ok := web.PathID(w, r, "proposal_id")
	if !ok {
		return
	}
	var in struct {
		Forever bool `json:"forever"`
	}
	if !web.DecodeOptionalJSON(w, r, &in) {
		return
	}
	ctx := detached(r)
	_, err := s.review.Dismiss(ctx, reviewapp.DismissInput{ProposalID: id, Suppress: in.Forever})
	if apiRefused(w, err, "not_dismissed", "Unknown approval.", nil) {
		return
	}
	web.JSON(w, http.StatusOK, map[string]any{"approval": s.currentApproval(ctx, id)})
}

func (s *service) apiResolve(w http.ResponseWriter, r *http.Request) {
	id, ok := web.PathID(w, r, "proposal_id")
	if !ok {
		return
	}
	var in struct {
		Outcome string `json:"outcome"`
	}
	if !web.DecodeOptionalJSON(w, r, &in) {
		return
	}
	ctx := detached(r)
	_, err := s.review.Resolve(ctx, reviewapp.ResolveInput{Target: reviewapp.Target{Kind: reviewapp.ProposalTarget, ID: id}, Outcome: in.Outcome})
	if apiRefused(w, err, "not_resolved", "Unknown approval.", nil) {
		return
	}
	web.JSON(w, http.StatusOK, map[string]any{"approval": s.currentApproval(ctx, id)})
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
	var cursor reviewapp.UndoCursor
	limit, after, ok := web.Paging(w, r, &cursor)
	if !ok {
		return
	}
	in := reviewapp.UndoableInput{ThreadID: thread, RunID: run, Limit: limit}
	if after {
		in.After = &cursor
	}
	found, err := s.review.Undoable(r.Context(), in)
	if err != nil {
		web.APIFail(w, err)
		return
	}
	out := web.List[reviewapp.UndoEntry]{Items: found.Data.Items}
	if found.Data.Next != nil {
		out.NextCursor = web.EncodeCursor(*found.Data.Next)
	}
	web.APIRead(w, r, found.Revision, out)
}

func (s *service) undoEffectEntry(ctx context.Context, id int64) (*reviewapp.UndoEntry, error) {
	e, err := s.ledger.Get(ctx, id)
	var missing *ledger.NotFound
	if errors.As(err, &missing) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entry, err := reviewapp.UndoEffectEntry(e)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

func (s *service) apiUndo(w http.ResponseWriter, r *http.Request) {
	const missing = "Unknown undo entry."
	source, raw, _ := strings.Cut(web.PathParam(r, "entry_id"), ":")
	id, perr := strconv.ParseInt(raw, 10, 64)
	if perr != nil || id < 1 {
		web.APIError(w, http.StatusNotFound, "not_found", missing)
		return
	}
	ctx := detached(r)
	switch source {
	case "write":
		known, err := s.undoEffectEntry(ctx, id)
		check(err)
		if known == nil {
			web.APIError(w, http.StatusNotFound, "not_found", missing)
			return
		}
		_, err = s.review.Undo(ctx, reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.EffectTarget, ID: id}})
		if apiRefused(w, err, "not_undone", missing, nil) {
			return
		}
		entry, err := s.undoEffectEntry(ctx, id)
		check(err)
		web.JSON(w, http.StatusOK, map[string]any{"entry": entry, "undone": true})
	case "proposal":
		done, err := s.review.Undo(ctx, reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.ProposalTarget, ID: id}})
		outcome := done.Data.Outcome
		var extra map[string]any
		if done.Data.Proposal != nil && (len(outcome.Outcomes) > 0 || outcome.SyncError != nil) {
			view := outcome.Dict()
			extra = map[string]any{"entry": reviewapp.UndoProposalEntry(done.Data.Proposal), "outcomes": view.Outcomes, "sync_error": view.SyncError}
		}
		if apiRefused(w, err, "not_undone", missing, extra) {
			return
		}
		out := map[string]any{"entry": reviewapp.UndoProposalEntry(done.Data.Proposal), "undone": outcome.Undone}
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
		web.APIError(w, http.StatusNotFound, "not_found", missing)
	}
}

func (s *service) apiEffects(w http.ResponseWriter, r *http.Request) {
	in := reviewapp.EffectsInput{
		Origin:      web.QueryValue(r.URL.Query(), "origin"),
		Operation:   web.QueryValue(r.URL.Query(), "operation"),
		BlockedOnly: web.QueryValue(r.URL.Query(), "blocked") == "true",
	}
	for name, dest := range map[string]**int64{"proposal_id": &in.ProposalID, "thread_id": &in.ThreadID, "run_id": &in.RunID} {
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
		in.BeforeID = n
	}
	if raw := web.QueryValue(r.URL.Query(), "limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			web.APIError(w, http.StatusUnprocessableEntity, "invalid", "limit must be a positive integer.", web.BadInt("query", "limit", raw))
			return
		}
		in.Limit = n
	}
	found, err := s.review.Effects(r.Context(), in)
	if err != nil {
		web.APIFail(w, err)
		return
	}
	web.APIRead(w, r, found.Revision, web.List[ledger.Effect]{Items: found.Data})
}

func (s *service) apiEffect(w http.ResponseWriter, r *http.Request) {
	id, ok := web.PathID(w, r, "effect_id")
	if !ok {
		return
	}
	found, err := s.review.Effect(r.Context(), reviewapp.EffectInput{ID: id})
	if err != nil {
		web.APIFail(w, err)
		return
	}
	web.APIRead(w, r, found.Revision, found.Data)
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
	done, err := s.review.Resolve(detached(r), reviewapp.ResolveInput{Target: reviewapp.Target{Kind: reviewapp.EffectTarget, ID: id}, Outcome: in.Outcome, Evidence: in.Evidence})
	if apiRefused(w, err, "not_resolved", "Unknown effect.", nil) {
		return
	}
	web.JSON(w, http.StatusOK, map[string]any{"effect": done.Data.Effect})
}

func (s *service) apiEffectUndo(w http.ResponseWriter, r *http.Request) {
	id, ok := web.PathID(w, r, "effect_id")
	if !ok {
		return
	}
	done, err := s.review.Undo(detached(r), reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.EffectTarget, ID: id}})
	if apiRefused(w, err, "not_undone", "Unknown effect.", nil) {
		return
	}
	web.JSON(w, http.StatusOK, map[string]any{"effect": done.Data.Effect, "undo": done.Data.Undo, "undone": true})
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
	return s.draftOf(ctx, *row)
}

func (s *service) draftOf(ctx context.Context, row drafts.Row) apiDraft {
	cards, err := s.review.DraftCards(ctx, []drafts.Row{row})
	check(err)
	return draftsOf(cards, reviewapp.AccountConfigured(ctx))[0]
}

func (s *service) apiDraft(w http.ResponseWriter, r *http.Request) {
	row, ok := s.apiDraftRow(w, r)
	if !ok {
		return
	}
	web.JSON(w, http.StatusOK, s.draftOf(r.Context(), *row))
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
	var disabled *module.OwnerDisabled
	switch {
	case errors.As(err, &disabled):
		web.APIError(w, http.StatusConflict, "module_disabled", err.Error())
		return
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
	var disabled *module.OwnerDisabled
	if errors.As(err, &refused) || errors.As(err, &disabled) {
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
	err := s.drafts.ResolveUnknown(ctx, row.ID, verdict, ledger.Resolver{Actor: "operator", Source: "ui"})
	var refused drafts.Error
	var disabled *module.OwnerDisabled
	if errors.As(err, &refused) || errors.As(err, &disabled) {
		web.APIError(w, http.StatusConflict, "not_resolved", err.Error())
		return
	}
	check(err)
	web.JSON(w, http.StatusOK, map[string]any{"draft": s.currentDraft(ctx, row.ID)})
}
