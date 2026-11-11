package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

type ResolutionRecord struct {
	ID               int64           `json:"id"`
	EffectID         *int64          `json:"effect_id"`
	ProposalID       *int64          `json:"proposal_id"`
	TS               float64         `json:"ts"`
	Actor            string          `json:"actor"`
	Source           string          `json:"source"`
	Outcome          string          `json:"outcome"`
	EffectiveOutcome string          `json:"effective_outcome"`
	PriorState       string          `json:"prior_state"`
	PriorError       string          `json:"prior_error"`
	Evidence         json.RawMessage `json:"evidence,omitempty"`
	Legacy           json.RawMessage `json:"legacy,omitempty"`
}

type Effect struct {
	ID               int64             `json:"id"`
	CreatedAt        float64           `json:"created_at"`
	SettledAt        *float64          `json:"settled_at"`
	LeaseAt          float64           `json:"lease_at"`
	Token            string            `json:"-"`
	Origin           string            `json:"origin"`
	Operation        string            `json:"operation"`
	OperationKey     string            `json:"operation_key"`
	Direction        string            `json:"direction"`
	UndoOf           *int64            `json:"undo_of"`
	ProposalID       *int64            `json:"proposal_id"`
	ThreadID         *int64            `json:"thread_id"`
	RunID            *int64            `json:"run_id"`
	Account          string            `json:"account"`
	MessageID        *int64            `json:"message_id"`
	Digest           string            `json:"digest"`
	DecisionID       *int64            `json:"decision_id"`
	Src              string            `json:"src"`
	Dest             string            `json:"dest"`
	Target           string            `json:"target"`
	Action           json.RawMessage   `json:"action"`
	Before           json.RawMessage   `json:"before,omitempty"`
	After            json.RawMessage   `json:"after,omitempty"`
	Undo             json.RawMessage   `json:"undo,omitempty"`
	State            State             `json:"state"`
	Error            string            `json:"error,omitempty"`
	UndoneBy         *int64            `json:"undone_by"`
	UndoneAt         *float64          `json:"undone_at"`
	Legacy           json.RawMessage   `json:"legacy,omitempty"`
	Resolution       *ResolutionRecord `json:"resolution,omitempty"`
	EffectiveOutcome string            `json:"effective_outcome,omitempty"`
	Blocked          bool              `json:"blocked"`
	Undoable         bool              `json:"undoable"`
}

const effectSelect = "SELECT e.id, e.created_at, e.settled_at, e.lease_at, e.token, e.origin, e.operation, e.operation_key, " +
	"e.direction, e.undo_of, e.proposal_id, e.thread_id, e.run_id, e.account, e.message_id, e.digest, e.decision_id, " +
	"e.src, e.dest, e.target, e.action_json, e.before_json, e.after_json, e.undo_json, e.state, e.error, e.undone_by, e.legacy_json, " +
	"r.id, r.effect_id, r.proposal_id, r.ts, r.actor, r.source, r.outcome, r.effective_outcome, r.prior_state, r.prior_error, " +
	"r.evidence_json, r.legacy_json, " +
	"(SELECT settled_at FROM effects u WHERE u.id = e.undone_by), " +
	"(SELECT COUNT(*) FROM effects u WHERE u.undo_of = e.id AND u.state IN ('intent','unknown','partial','done') " +
	"AND NOT EXISTS (SELECT 1 FROM effect_resolutions ur WHERE ur.effect_id = u.id AND ur.effective_outcome = 'failed')) " +
	"FROM effects e LEFT JOIN effect_resolutions r ON r.id = " +
	"(SELECT MAX(id) FROM effect_resolutions WHERE effect_id = e.id)"

type scanner interface{ Scan(...any) error }

func scanEffect(sc scanner) (Effect, error) {
	var e Effect
	var settledAt, undoneAt sql.NullFloat64
	var undoOf, proposalID, threadID, runID, messageID, decisionID, undoneBy sql.NullInt64
	var account, digest, src, dest, errorText sql.NullString
	var action, before, after, undo, legacy sql.NullString
	var rID, rEffectID, rProposalID sql.NullInt64
	var rTS sql.NullFloat64
	var rActor, rSource, rOutcome, rEffective, rPriorState, rPriorError, rEvidence, rLegacy sql.NullString
	var activeUndo int64
	err := sc.Scan(&e.ID, &e.CreatedAt, &settledAt, &e.LeaseAt, &e.Token, &e.Origin, &e.Operation, &e.OperationKey,
		&e.Direction, &undoOf, &proposalID, &threadID, &runID, &account, &messageID, &digest, &decisionID,
		&src, &dest, &e.Target, &action, &before, &after, &undo, &e.State, &errorText, &undoneBy, &legacy,
		&rID, &rEffectID, &rProposalID, &rTS, &rActor, &rSource, &rOutcome, &rEffective, &rPriorState, &rPriorError,
		&rEvidence, &rLegacy, &undoneAt, &activeUndo)
	if err != nil {
		return e, err
	}
	e.SettledAt = floatPtr(settledAt)
	e.UndoOf = intPtr(undoOf)
	e.ProposalID = intPtr(proposalID)
	e.ThreadID = intPtr(threadID)
	e.RunID = intPtr(runID)
	e.MessageID = intPtr(messageID)
	e.DecisionID = intPtr(decisionID)
	e.UndoneBy = intPtr(undoneBy)
	e.UndoneAt = floatPtr(undoneAt)
	e.Account = account.String
	e.Digest = digest.String
	e.Src = src.String
	e.Dest = dest.String
	e.Error = errorText.String
	e.Action = raw(action)
	e.Before = raw(before)
	e.After = raw(after)
	e.Undo = raw(undo)
	e.Legacy = raw(legacy)
	if rID.Valid {
		e.Resolution = &ResolutionRecord{
			ID: rID.Int64, EffectID: intPtr(rEffectID), ProposalID: intPtr(rProposalID), TS: rTS.Float64,
			Actor: rActor.String, Source: rSource.String, Outcome: rOutcome.String, EffectiveOutcome: rEffective.String,
			PriorState: rPriorState.String, PriorError: rPriorError.String, Evidence: raw(rEvidence), Legacy: raw(rLegacy),
		}
	}
	if e.Resolution != nil {
		e.EffectiveOutcome = e.Resolution.EffectiveOutcome
	} else if e.State == Done || e.State == Failed || e.State == Partial {
		e.EffectiveOutcome = string(e.State)
	}
	e.Blocked = e.State.Blocking() && e.Resolution == nil
	e.Undoable = e.Direction == "forward" && e.UndoneBy == nil && activeUndo == 0 &&
		(e.State == Done || e.State == Partial) && len(e.Undo) > 0 &&
		(e.Resolution == nil || e.Resolution.EffectiveOutcome != "failed")
	return e, nil
}

func (s *Store) decorateUndoable(e *Effect) {
	if !e.Undoable {
		return
	}
	var head struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(e.Undo, &head); err != nil || head.Op == "" {
		e.Undoable = false
		return
	}
	if _, ok := s.undoer(head.Op); !ok {
		e.Undoable = false
	}
}

func (s *Store) Get(ctx context.Context, id int64) (Effect, error) {
	e, err := scanEffect(s.DB.Read.QueryRowContext(ctx, effectSelect+" WHERE e.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Effect{}, &NotFound{ID: id}
	}
	if err != nil {
		return Effect{}, err
	}
	s.decorateUndoable(&e)
	return e, nil
}

func (s *Store) List(ctx context.Context, f Filter) ([]Effect, error) {
	where := []string{"1 = 1"}
	var args []any
	if f.Origin != "" {
		where = append(where, "e.origin = ?")
		args = append(args, f.Origin)
	}
	if f.Operation != "" {
		where = append(where, "e.operation = ?")
		args = append(args, f.Operation)
	}
	if f.ProposalID != nil {
		where = append(where, "e.proposal_id = ?")
		args = append(args, *f.ProposalID)
	}
	if f.ThreadID != nil {
		where = append(where, "e.thread_id = ?")
		args = append(args, *f.ThreadID)
	}
	if f.RunID != nil {
		where = append(where, "e.run_id = ?")
		args = append(args, *f.RunID)
	}
	if f.BeforeID > 0 {
		where = append(where, "e.id < ?")
		args = append(args, f.BeforeID)
	}
	if f.BlockedOnly {
		where = append(where, "e.state IN ('intent','unknown','partial') AND r.id IS NULL")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	query := effectSelect + " WHERE " + strings.Join(where, " AND ") + " ORDER BY e.id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.DB.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out, err := db.Collect(rows, func(r *sql.Rows) (Effect, error) { return scanEffect(r) })
	if err != nil {
		return nil, err
	}
	for i := range out {
		s.decorateUndoable(&out[i])
	}
	return out, nil
}

func (s *Store) Blocked(ctx context.Context, resources []string) ([]Effect, error) {
	where := []string{"e.state IN ('intent','unknown','partial')", "r.id IS NULL"}
	var args []any
	if len(resources) > 0 {
		where = append(where, "EXISTS (SELECT 1 FROM effect_resources er WHERE er.effect_id = e.id AND er.holding = 1 "+
			"AND er.resource IN ("+placeholders(len(resources))+"))")
		for _, resource := range resources {
			args = append(args, resource)
		}
	}
	query := effectSelect + " WHERE " + strings.Join(where, " AND ") + " ORDER BY e.id DESC"
	rows, err := s.DB.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out, err := db.Collect(rows, func(r *sql.Rows) (Effect, error) { return scanEffect(r) })
	if err != nil {
		return nil, err
	}
	for i := range out {
		s.decorateUndoable(&out[i])
	}
	return out, nil
}

func (s *Store) ProposalEffects(ctx context.Context, id int64) ([]Effect, error) {
	return s.List(ctx, Filter{ProposalID: &id, Limit: 500})
}

func ThreadsForTargets(ctx context.Context, q db.Querier, targets []string) ([]int64, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	args := make([]any, len(targets))
	for i, target := range targets {
		args[i] = target
	}
	rows, err := q.QueryContext(ctx,
		"SELECT DISTINCT thread_id FROM effects WHERE direction = 'forward' AND state = 'done' AND undone_by IS NULL "+
			"AND thread_id IS NOT NULL AND target IN ("+placeholders(len(targets))+")", args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func intPtr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func floatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

func raw(v sql.NullString) json.RawMessage {
	if !v.Valid || v.String == "" {
		return nil
	}
	return json.RawMessage(v.String)
}
