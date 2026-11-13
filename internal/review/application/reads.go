package application

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/serverops"
	"github.com/jeiang/atrium/internal/textx"
)

const (
	DraftLimit    = 100
	findingsLimit = 20
)

var DirectOrigins = []string{"agent", "local", "calendar", "export", "tracking", "media", "notify"}

var pendingScopes = []string{state.Review, state.Effects, state.Mail, state.Calendar, state.Tasks, state.Assistant, state.Settings, state.Connections}

type Arg struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type Card struct {
	ID            int64
	Kind          string
	KindLabel     string
	KindIcon      string
	Module        string
	Approval      bool
	Status        string
	Account       *string
	Title         string
	ThreadID      *int64
	RunID         *int64
	LinkThread    *int64
	CreatedAt     float64
	DecidedAt     *float64
	AppliedAt     *float64
	UndoneAt      *float64
	Undoable      bool
	Blocked       bool
	BlockedEffect bool
	BlockedUndo   bool
	BlockedError  string
	Args          []Arg
	Preview       any
	Result        any
}

type DraftCard struct {
	Row        drafts.Row
	Unexpected []string
}

type Finding struct {
	ID       int64
	Severity string
	Host     string
	Metric   string
	Title    string
	Body     string
	Created  float64
}

type PendingInput struct {
	Findings bool
}

type Pending struct {
	Approvals  []Card
	Blocked    []Card
	Drafts     []DraftCard
	Count      int
	Findings   []Finding
	Configured map[string]bool
}

func (s *Service) Pending(ctx context.Context, in PendingInput) (application.Read[Pending], error) {
	scopes := pendingScopes
	if in.Findings {
		scopes = append(slices.Clone(scopes), state.Ops)
	}
	return read(ctx, s, PendingName, scopes, in, func() (Pending, error) { return s.pending(ctx, in) }, nil)
}

func (s *Service) pending(ctx context.Context, in PendingInput) (Pending, error) {
	q := s.db.Read
	var out Pending
	pending, err := proposals.List(ctx, q, proposals.Pending, "")
	if err != nil {
		return out, err
	}
	blocked, err := proposals.ListBlocked(ctx, q)
	if err != nil {
		return out, err
	}
	if out.Approvals, err = s.cards(ctx, pending); err != nil {
		return out, err
	}
	if out.Blocked, err = s.cards(ctx, blocked); err != nil {
		return out, err
	}
	open, err := s.drafts.List(ctx, drafts.Draft, DraftLimit)
	if err != nil {
		return out, err
	}
	unknown, err := s.drafts.List(ctx, drafts.Unknown, DraftLimit)
	if err != nil {
		return out, err
	}
	rows := append(unknown, open...)
	slices.SortStableFunc(rows, func(a, b drafts.Row) int { return cmp.Compare(b.UpdatedAt, a.UpdatedAt) })
	if out.Drafts, err = s.DraftCards(ctx, rows); err != nil {
		return out, err
	}
	n, err := proposals.NeedsCount(ctx, q)
	if err != nil {
		return out, err
	}
	d, err := drafts.CountNeeds(ctx, q)
	if err != nil {
		return out, err
	}
	out.Count = n + d
	if in.Findings {
		found, err := serverops.ReviewFindings(ctx, q, findingsLimit)
		if err != nil {
			return out, err
		}
		out.Findings = make([]Finding, 0, len(found))
		for _, f := range found {
			finding := Finding{ID: f.ID, Severity: f.Severity, Title: f.Title, Body: f.Body, Created: f.CreatedAt}
			finding.Host, finding.Metric, _ = strings.Cut(f.Fact, ":")
			out.Findings = append(out.Findings, finding)
		}
	}
	out.Configured = AccountConfigured(ctx)
	return out, nil
}

func (s *Service) DraftCards(ctx context.Context, rows []drafts.Row) ([]DraftCard, error) {
	unexpected, err := s.drafts.UnexpectedRecipientsBatch(ctx, rows)
	if err != nil {
		return nil, err
	}
	out := make([]DraftCard, len(rows))
	for i, row := range rows {
		out[i] = DraftCard{Row: row, Unexpected: unexpected[i]}
	}
	return out, nil
}

func AccountConfigured(ctx context.Context) map[string]bool {
	accounts := config.From(ctx).Accounts()
	out := make(map[string]bool, len(accounts))
	for _, spec := range accounts {
		_, _, ok := config.Credential(ctx, spec.Name)
		out[spec.Name] = ok
	}
	return out
}

func (s *Service) cards(ctx context.Context, list []*proposals.Proposal) ([]Card, error) {
	out := make([]Card, len(list))
	for i, p := range list {
		full, err := proposals.Get(ctx, s.db.Read, p.ID)
		if err != nil {
			return nil, err
		}
		if full == nil {
			full = p
		}
		out[i] = s.CardOf(full)
	}
	return out, nil
}

func (s *Service) CardOf(p *proposals.Proposal) Card {
	kind, _ := s.registry.Kind(p.Kind)
	thread, run := payloadRefs(p.Payload)
	out := Card{
		ID: p.ID, Kind: p.Kind, KindLabel: kind.Label, KindIcon: kind.Icon, Module: kind.Module, Approval: kind.Approval,
		Status: p.Status, Account: p.Account, Title: p.Title, ThreadID: thread, RunID: run, CreatedAt: p.CreatedTS,
		DecidedAt: p.DecidedTS, AppliedAt: p.AppliedTS, UndoneAt: p.UndoneTS, Undoable: p.Undoable, Blocked: p.Blocked,
		BlockedEffect: p.BlockedEffect, BlockedUndo: p.BlockedUndo, BlockedError: p.BlockedError,
		Args: []Arg{}, Preview: plainJSON(p.Preview), Result: plainJSON(p.Result),
	}
	if kind.Approval {
		out.Args = approvalArgs(p.Preview, p.Kind != permission.Kind)
	}
	return out
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

func approvalArgs(preview any, clip bool) []Arg {
	d, _ := preview.(map[string]any)
	args, _ := d["args"].(map[string]any)
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]Arg, 0, len(keys))
	for _, k := range keys {
		label := strings.ReplaceAll(k, "_", " ")
		r, size := utf8.DecodeRuneInString(label)
		out = append(out, Arg{Label: strings.ToUpper(string(r)) + label[size:], Value: argText(args[k], clip)})
	}
	return out
}

func argText(v any, clip bool) string {
	var text string
	switch x := v.(type) {
	case string:
		text = x
	case bool:
		text = "No"
		if x {
			text = "Yes"
		}
	case int64:
		text = fmt.Sprint(x)
	default:
		text = jsonx.Dumps(x)
	}
	if clip && utf8.RuneCountInString(text) > 200 {
		return textx.Clip(text, 199) + "…"
	}
	return text
}

func plainJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = plainJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = plainJSON(e)
		}
		return out
	}
	return v
}

type ProposalInput struct {
	ID int64
}

func (s *Service) Proposal(ctx context.Context, in ProposalInput) (application.Read[Card], error) {
	if in.ID < 1 {
		return application.Read[Card]{}, invalid("proposal_id", "proposal_id must be a positive integer.")
	}
	return read(ctx, s, ProposalName, []string{state.Review, state.Effects}, in, func() (Card, error) {
		found, err := proposals.Get(ctx, s.db.Read, in.ID)
		if err != nil {
			return Card{}, err
		}
		if found == nil {
			return Card{}, notFound("Unknown approval.")
		}
		out := s.CardOf(found)
		thread, known, err := proposals.ThreadOf(ctx, s.db.Read, in.ID)
		if err != nil {
			return Card{}, err
		}
		if known {
			out.LinkThread = &thread
		}
		return out, nil
	}, nil)
}

type ProposalsInput struct {
	Status string
	Kind   string
}

type ProposalSummary struct {
	ID       int64   `json:"id"`
	Kind     string  `json:"kind"`
	Account  *string `json:"account"`
	Status   string  `json:"status"`
	Title    string  `json:"title"`
	Undoable bool    `json:"undoable"`
}

func (s *Service) Proposals(ctx context.Context, in ProposalsInput) (application.Read[[]ProposalSummary], error) {
	if in.Status != "" && !slices.Contains(proposals.Statuses[:], in.Status) {
		return application.Read[[]ProposalSummary]{}, invalid("status", "Unknown proposal status.")
	}
	return read(ctx, s, ProposalsName, []string{state.Review, state.Effects}, in, func() ([]ProposalSummary, error) {
		list, err := proposals.List(ctx, s.db.Read, in.Status, in.Kind)
		if err != nil {
			return nil, err
		}
		out := make([]ProposalSummary, len(list))
		for i, p := range list {
			out[i] = ProposalSummary{ID: p.ID, Kind: p.Kind, Account: p.Account, Status: p.Status, Title: p.Title, Undoable: p.Undoable}
		}
		return out, nil
	}, nil)
}

type UndoEntry struct {
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

type UndoCursor struct {
	TS     float64 `json:"t"`
	Source int     `json:"s"`
	ID     int64   `json:"i"`
}

const (
	undoWrite    = 0
	undoProposal = 1
)

type UndoableInput struct {
	ThreadID *int64
	RunID    *int64
	Limit    int
	After    *UndoCursor
}

type Undoable struct {
	Items []UndoEntry
	Next  *UndoCursor
}

func UndoEffectEntry(e ledger.Effect) (UndoEntry, error) {
	var entry UndoEntry
	entry.TS, entry.ThreadID, entry.RunID, entry.Tool, entry.Target = e.CreatedAt, e.ThreadID, e.RunID, e.Operation, e.Target
	entry.UndoneAt, entry.State = e.UndoneAt, string(e.State)
	if e.Error != "" {
		text := e.Error
		entry.Error = &text
	}
	var err error
	if entry.Before, err = Snippet(RawText(e.Before)); err != nil {
		return entry, err
	}
	if entry.After, err = Snippet(RawText(e.After)); err != nil {
		return entry, err
	}
	entry.ID = "write:" + strconv.FormatInt(e.ID, 10)
	entry.Source = SourceWrite
	entry.Undoable = e.Undoable
	return entry, nil
}

func UndoProposalEntry(p *proposals.Proposal) UndoEntry {
	thread, run := payloadRefs(p.Payload)
	ts := p.CreatedTS
	if p.AppliedTS != nil {
		ts = *p.AppliedTS
	}
	return UndoEntry{
		ID: "proposal:" + strconv.FormatInt(p.ID, 10), Source: "proposal", Tool: p.Kind, Target: p.Title, TS: ts,
		ThreadID: thread, RunID: run, State: p.Status, UndoneAt: p.UndoneTS, Undoable: p.Undoable && !p.BlockedUndo,
	}
}

func (s *Service) Undoable(ctx context.Context, in UndoableInput) (application.Read[Undoable], error) {
	return read(ctx, s, UndoableName, []string{state.Review, state.Effects}, in, func() (Undoable, error) { return s.undoable(ctx, in) }, nil)
}

func (s *Service) undoable(ctx context.Context, in UndoableInput) (Undoable, error) {
	limit := in.Limit
	var propWhere []string
	var propArgs []any
	if in.ThreadID != nil {
		propWhere = append(propWhere, "json_extract(payload, '$.thread_id') = ?")
		propArgs = append(propArgs, *in.ThreadID)
	}
	if in.RunID != nil {
		propWhere = append(propWhere, "json_extract(payload, '$.run_id') = ?")
		propArgs = append(propArgs, *in.RunID)
	}
	filter := ledger.Filter{Origins: DirectOrigins, Limit: limit + 1, ThreadID: in.ThreadID, RunID: in.RunID}
	if c := in.After; c != nil {
		propWhere = append(propWhere, "(COALESCE(applied_ts, created_ts) < ? OR (COALESCE(applied_ts, created_ts) = ? AND ? = 1 AND id < ?))")
		propArgs = append(propArgs, c.TS, c.TS, c.Source, c.ID)
		filter.BeforeTS = c.TS
		if c.Source == undoProposal {
			filter.BeforeID = math.MaxInt64
		} else {
			filter.BeforeID = c.ID
		}
	}
	found, err := s.ledger.List(ctx, filter)
	if err != nil {
		return Undoable{}, err
	}
	written := make([]UndoEntry, 0, len(found))
	for _, e := range found {
		if !e.Undoable {
			continue
		}
		entry, err := UndoEffectEntry(e)
		if err != nil {
			return Undoable{}, err
		}
		written = append(written, entry)
	}
	applied, err := proposals.ListUndoable(ctx, s.db.Read, strings.Join(propWhere, " AND "), propArgs, limit+1)
	if err != nil {
		return Undoable{}, err
	}
	keys := make([]UndoCursor, 0, len(written)+len(applied))
	entries := make([]UndoEntry, 0, len(written)+len(applied))
	i, j := 0, 0
	for len(entries) < limit+1 && (i < len(written) || j < len(applied)) {
		var next *UndoEntry
		if j < len(applied) {
			p := UndoProposalEntry(applied[j])
			next = &p
		}
		if next != nil && (i >= len(written) || next.TS >= written[i].TS) {
			entries = append(entries, *next)
			keys = append(keys, UndoCursor{TS: next.TS, Source: undoProposal, ID: applied[j].ID})
			j++
			continue
		}
		entries = append(entries, written[i])
		id, _ := strconv.ParseInt(strings.TrimPrefix(written[i].ID, "write:"), 10, 64)
		keys = append(keys, UndoCursor{TS: written[i].TS, Source: undoWrite, ID: id})
		i++
	}
	out := Undoable{Items: entries}
	if len(entries) > limit {
		out.Items = entries[:limit]
		out.Next = &keys[limit-1]
	}
	return out, nil
}

type EffectsInput struct {
	Origin      string
	Origins     []string
	Operation   string
	ProposalID  *int64
	ThreadID    *int64
	RunID       *int64
	BeforeID    int64
	Limit       int
	BlockedOnly bool
}

type leaseMark struct {
	ID          int64   `json:"id"`
	LeaseAt     float64 `json:"lease_at"`
	UndoLeaseAt float64 `json:"undo_lease_at"`
	Blocked     bool    `json:"blocked"`
	UndoBlocked bool    `json:"undo_blocked"`
}

func leaseMarks(effects []ledger.Effect) any {
	out := make([]leaseMark, len(effects))
	for i, e := range effects {
		out[i] = leaseMark{ID: e.ID, LeaseAt: e.LeaseAt, UndoLeaseAt: e.UndoLeaseAt, Blocked: e.Blocked, UndoBlocked: e.UndoBlocked}
	}
	return out
}

func (s *Service) Effects(ctx context.Context, in EffectsInput) (application.Read[[]ledger.Effect], error) {
	if in.BeforeID < 0 {
		return application.Read[[]ledger.Effect]{}, invalid("before", "before must be a non-negative integer.")
	}
	return read(ctx, s, EffectsName, []string{state.Effects}, in, func() ([]ledger.Effect, error) {
		return s.ledger.List(ctx, ledger.Filter{
			Origin: in.Origin, Origins: in.Origins, Operation: in.Operation, ProposalID: in.ProposalID, ThreadID: in.ThreadID,
			RunID: in.RunID, BeforeID: in.BeforeID, Limit: in.Limit, BlockedOnly: in.BlockedOnly,
		})
	}, leaseMarks)
}

type EffectInput struct {
	ID int64
}

func (s *Service) Effect(ctx context.Context, in EffectInput) (application.Read[ledger.Effect], error) {
	return read(ctx, s, EffectName, []string{state.Effects}, in, func() (ledger.Effect, error) {
		found, err := s.ledger.Get(ctx, in.ID)
		var missing *ledger.NotFound
		if errors.As(err, &missing) {
			return ledger.Effect{}, notFound("Unknown effect.")
		}
		return found, err
	}, func(e ledger.Effect) any { return leaseMarks([]ledger.Effect{e}) })
}
