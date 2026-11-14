package reviewpages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/jsonx"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
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
	Preview       json.RawMessage `json:"preview"`
	Result        json.RawMessage `json:"result"`
}

type Draft = apiDraft

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

func approvalOf(c reviewapp.Card) apiApproval {
	return apiApproval{
		ID: c.ID, Kind: c.Kind, KindLabel: c.KindLabel, Module: c.Module, Approval: c.Approval, Status: c.Status,
		Account: c.Account, Title: c.Title, ThreadID: c.ThreadID, RunID: c.RunID, CreatedAt: c.CreatedAt, DecidedAt: c.DecidedAt,
		AppliedAt: c.AppliedAt, UndoneAt: c.UndoneAt, Undoable: c.Undoable, Blocked: c.Blocked, BlockedEffect: c.BlockedEffect, BlockedUndo: c.BlockedUndo,
		Args: c.Args, Preview: apiRaw(c.Preview), Result: apiRaw(c.Result),
	}
}

func approvalsOf(cards []reviewapp.Card) []apiApproval {
	out := make([]apiApproval, len(cards))
	for i, c := range cards {
		out[i] = approvalOf(c)
	}
	return out
}

func DraftsOf(cards []reviewapp.DraftCard, configured map[string]bool) []Draft {
	out := make([]Draft, len(cards))
	for i, c := range cards {
		row := c.Row
		recipients := c.Unexpected
		if recipients == nil {
			recipients = []string{}
		}
		out[i] = Draft{
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
	Drafts    []Draft       `json:"drafts"`
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
		if c.Row.Status == mailapp.DraftUnknown {
			unknown++
		}
	}
	web.APIRead(w, r, found.Revision, pendingBody{
		Approvals: approvalsOf(data.Approvals),
		Blocked:   approvalsOf(data.Blocked),
		Drafts:    DraftsOf(data.Drafts, data.Configured),
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
	e, err := s.review.StoredEffect(ctx, id)
	var missing *reviewapp.EffectNotFound
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
	web.APIRead(w, r, found.Revision, web.List[reviewapp.Effect]{Items: found.Data})
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

func apiRaw(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	check(err)
	return raw
}

type apiUndoRequest struct {
	ThreadID string `query:"thread_id" json:"-"`
	RunID    string `query:"run_id" json:"-"`
	Limit    string `query:"limit" json:"-"`
	Cursor   string `query:"cursor" json:"-"`
}
type apiEffectsRequest struct {
	Origin     string `query:"origin" json:"-"`
	Operation  string `query:"operation" json:"-"`
	Blocked    string `query:"blocked" json:"-"`
	ProposalID string `query:"proposal_id" json:"-"`
	ThreadID   string `query:"thread_id" json:"-"`
	RunID      string `query:"run_id" json:"-"`
	Before     string `query:"before" json:"-"`
	Limit      string `query:"limit" json:"-"`
}
type apiEffectRequest struct {
	EffectID int64 `path:"effect_id" json:"-"`
}

func APIReadBindings() []application.Binding {
	return []application.Binding{
		{Kind: application.API, Target: "GET /pending", Uses: []application.Name{reviewapp.PendingName}, API: &application.APIShape{Area: "review", Request: reflect.TypeFor[struct{}](), Response: reflect.TypeFor[pendingBody](), Body: application.BodyNone, Success: 200}},
		{Kind: application.API, Target: "GET /undo", Uses: []application.Name{reviewapp.UndoableName}, API: &application.APIShape{Area: "activity", Request: reflect.TypeFor[apiUndoRequest](), Response: reflect.TypeFor[web.List[reviewapp.UndoEntry]](), Body: application.BodyNone, Success: 200, Errors: []application.Code{application.Invalid}}},
		{Kind: application.API, Target: "GET /effects", Uses: []application.Name{reviewapp.EffectsName}, API: &application.APIShape{Area: "activity", Request: reflect.TypeFor[apiEffectsRequest](), Response: reflect.TypeFor[web.List[reviewapp.Effect]](), Body: application.BodyNone, Success: 200, Errors: []application.Code{application.Invalid}}},
		{Kind: application.API, Target: "GET /effects/{effect_id}", Uses: []application.Name{reviewapp.EffectName}, API: &application.APIShape{Area: "activity", Request: reflect.TypeFor[apiEffectRequest](), Response: reflect.TypeFor[reviewapp.Effect](), Body: application.BodyNone, Success: 200, Errors: []application.Code{application.NotFound}}},
	}
}
