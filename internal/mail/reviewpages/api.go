package reviewpages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/writes"
)

var appResolver = proposals.Resolver{Actor: "operator", Source: "ui"}

type apiApproval struct {
	ID          int64    `json:"id"`
	Kind        string   `json:"kind"`
	KindLabel   string   `json:"kind_label"`
	Module      string   `json:"module"`
	Approval    bool     `json:"approval"`
	Status      string   `json:"status"`
	Account     *string  `json:"account"`
	Title       string   `json:"title"`
	ThreadID    *int64   `json:"thread_id"`
	RunID       *int64   `json:"run_id"`
	CreatedAt   float64  `json:"created_at"`
	DecidedAt   *float64 `json:"decided_at"`
	AppliedAt   *float64 `json:"applied_at"`
	UndoneAt    *float64 `json:"undone_at"`
	Undoable    bool     `json:"undoable"`
	Blocked     bool     `json:"blocked"`
	BlockedUndo bool     `json:"blocked_undo"`
	Args        []Arg    `json:"args"`
	Preview     any      `json:"preview"`
	Result      any      `json:"result"`
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
		AppliedAt: p.AppliedTS, UndoneAt: p.UndoneTS, Undoable: p.Undoable, Blocked: p.Blocked, BlockedUndo: p.BlockedUndo,
		Args: []Arg{}, Preview: plainJSON(p.Preview), Result: plainJSON(p.Result),
	}
	if kind.Approval {
		out.Args = approvalArgs(p.Preview)
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
	err := proposals.Dismiss(ctx, s.deps.DB, found.ID, status, appResolver, s.now())
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

const undoWriteColumns = "id, ts, thread_id, run_id, tool, target, before_json, after_json, undo_json, undone_at, state, error"

func scanUndoWrite(sc interface{ Scan(...any) error }) (apiUndoEntry, error) {
	var e apiUndoEntry
	var id int64
	var before, after, undo *string
	if err := sc.Scan(&id, &e.TS, &e.ThreadID, &e.RunID, &e.Tool, &e.Target, &before, &after, &undo, &e.UndoneAt, &e.State, &e.Error); err != nil {
		return e, err
	}
	var err error
	if e.Before, err = snippet(before); err != nil {
		return e, err
	}
	if e.After, err = snippet(after); err != nil {
		return e, err
	}
	e.ID = "write:" + strconv.FormatInt(id, 10)
	e.Source = sourceWrite
	e.Undoable = undo != nil && *undo != "" && e.UndoneAt == nil && e.State != "unknown"
	return e, nil
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
	writeWhere := []string{"undo_json IS NOT NULL", "undo_json <> ''"}
	var writeArgs []any
	var propWhere []string
	var propArgs []any
	if thread != nil {
		writeWhere = append(writeWhere, "thread_id = ?")
		writeArgs = append(writeArgs, *thread)
		propWhere = append(propWhere, "json_extract(payload, '$.thread_id') = ?")
		propArgs = append(propArgs, *thread)
	}
	if run != nil {
		writeWhere = append(writeWhere, "run_id = ?")
		writeArgs = append(writeArgs, *run)
		propWhere = append(propWhere, "json_extract(payload, '$.run_id') = ?")
		propArgs = append(propArgs, *run)
	}
	if after {
		writeWhere = append(writeWhere, "(ts < ? OR (ts = ? AND (? > 0 OR id < ?)))")
		writeArgs = append(writeArgs, cursor.TS, cursor.TS, cursor.Source, cursor.ID)
		propWhere = append(propWhere, "(COALESCE(applied_ts, created_ts) < ? OR (COALESCE(applied_ts, created_ts) = ? AND ? = 1 AND id < ?))")
		propArgs = append(propArgs, cursor.TS, cursor.TS, cursor.Source, cursor.ID)
	}
	rows, err := s.deps.DB.Read.QueryContext(ctx, "SELECT "+undoWriteColumns+" FROM agent_writes WHERE "+strings.Join(writeWhere, " AND ")+
		" ORDER BY ts DESC, id DESC LIMIT ?", append(writeArgs, limit+1)...)
	check(err)
	written, err := db.Collect(rows, func(r *sql.Rows) (apiUndoEntry, error) { return scanUndoWrite(r) })
	check(err)
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

func (s *service) undoWriteEntry(ctx context.Context, id int64) (*apiUndoEntry, error) {
	e, err := scanUndoWrite(s.deps.DB.Read.QueryRowContext(ctx, "SELECT "+undoWriteColumns+" FROM agent_writes WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
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
		known, err := s.undoWriteEntry(ctx, id)
		check(err)
		if known == nil {
			web.APIError(w, http.StatusNotFound, "not_found", "Unknown undo entry.")
			return
		}
		_, err = s.writes.Undo(ctx, s.deps.DB, id, s.now)
		var refused writes.UndoError
		if errors.As(err, &refused) {
			web.APIError(w, http.StatusConflict, "not_undone", writeUndoDetail(err))
			return
		}
		check(err)
		entry, err := s.undoWriteEntry(ctx, id)
		check(err)
		web.JSON(w, http.StatusOK, map[string]any{"entry": entry, "undone": true})
	case "proposal":
		known, err := proposals.Get(ctx, s.deps.DB.Read, id)
		check(err)
		if known == nil {
			web.APIError(w, http.StatusNotFound, "not_found", "Unknown undo entry.")
			return
		}
		outcome, err := s.pipeline.Undo(ctx, id)
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
