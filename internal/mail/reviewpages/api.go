package reviewpages

import (
	"encoding/json"
	"net/http"
	"strconv"

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

type (
	Approval        = apiApproval
	Pending         = pendingBody
	UndoFeedRequest = apiUndoRequest
	EffectsRequest  = apiEffectsRequest
	EffectRequest   = apiEffectRequest
)

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

func ApprovalOf(c reviewapp.Card) Approval { return approvalOf(c) }

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

func PendingRead(w http.ResponseWriter, r *http.Request, s *reviewapp.Service) {
	found, err := s.Pending(r.Context(), reviewapp.PendingInput{Refs: true})
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

func UndoFeed(w http.ResponseWriter, r *http.Request, s *reviewapp.Service) {
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
	found, err := s.Undoable(r.Context(), in)
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

func EffectsRead(w http.ResponseWriter, r *http.Request, s *reviewapp.Service) {
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
	found, err := s.Effects(r.Context(), in)
	if err != nil {
		web.APIFail(w, err)
		return
	}
	web.APIRead(w, r, found.Revision, web.List[reviewapp.Effect]{Items: found.Data})
}

func EffectRead(w http.ResponseWriter, r *http.Request, s *reviewapp.Service) {
	id, ok := web.PathID(w, r, "effect_id")
	if !ok {
		return
	}
	found, err := s.Effect(r.Context(), reviewapp.EffectInput{ID: id})
	if err != nil {
		web.APIFail(w, err)
		return
	}
	web.APIRead(w, r, found.Revision, found.Data)
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
