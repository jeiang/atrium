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
	if len(f.Origins) > 0 {
		where = append(where, "e.origin IN ("+placeholders(len(f.Origins))+")")
		for _, origin := range f.Origins {
			args = append(args, origin)
		}
	}
	if f.Operation != "" {
		where = append(where, "e.operation = ?")
		args = append(args, f.Operation)
	}
	if len(f.OperationLike) > 0 {
		likes := make([]string, len(f.OperationLike))
		for i, pattern := range f.OperationLike {
			likes[i] = "e.operation LIKE ?"
			args = append(args, pattern)
		}
		where = append(where, "("+strings.Join(likes, " OR ")+")")
	}
	for _, pattern := range f.OperationNotLike {
		where = append(where, "e.operation NOT LIKE ?")
		args = append(args, pattern)
	}
	if f.Target != "" {
		where = append(where, "e.target = ?")
		args = append(args, f.Target)
	}
	if f.Since > 0 {
		where = append(where, "e.created_at >= ?")
		args = append(args, f.Since)
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
	if f.BeforeTS > 0 {
		if f.BeforeID > 0 {
			where = append(where, "(e.created_at < ? OR (e.created_at = ? AND e.id < ?))")
			args = append(args, f.BeforeTS, f.BeforeTS, f.BeforeID)
		} else {
			where = append(where, "e.created_at < ?")
			args = append(args, f.BeforeTS)
		}
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

func CountForRuns(ctx context.Context, q db.Querier, runIDs []int64, operation string) (int, error) {
	if len(runIDs) == 0 {
		return 0, nil
	}
	args := make([]any, len(runIDs))
	for i, id := range runIDs {
		args[i] = id
	}
	args = append(args, operation)
	var n int
	err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM effects WHERE direction = 'forward' AND run_id IN ("+placeholders(len(runIDs))+") AND operation = ?",
		args...).Scan(&n)
	return n, err
}

func TargetsForRuns(ctx context.Context, q db.Querier, runIDs []int64, operation string) ([]string, error) {
	if len(runIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(runIDs))
	for i, id := range runIDs {
		args[i] = id
	}
	args = append(args, operation)
	rows, err := q.QueryContext(ctx,
		"SELECT DISTINCT target FROM effects WHERE direction = 'forward' AND run_id IN ("+placeholders(len(runIDs))+") AND operation = ?",
		args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var target string
		return target, r.Scan(&target)
	})
}

func TargetsLike(ctx context.Context, q db.Querier, like, excludeOperation string) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT DISTINCT target FROM effects WHERE direction = 'forward' AND target LIKE ? AND operation != ?", like, excludeOperation)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var target string
		return target, r.Scan(&target)
	})
}

const (
	effectBlockedExpr  = "e.state IN ('intent','unknown','partial') AND r.id IS NULL"
	effectUndoableExpr = "e.direction = 'forward' AND e.state IN ('done','partial') AND e.undo_json IS NOT NULL " +
		"AND e.undone_by IS NULL AND (r.id IS NULL OR r.effective_outcome <> 'failed') " +
		"AND NOT EXISTS (SELECT 1 FROM effects u WHERE u.undo_of = e.id AND u.state IN ('intent','unknown','partial','done') " +
		"AND NOT EXISTS (SELECT 1 FROM effect_resolutions ur WHERE ur.effect_id = u.id AND ur.effective_outcome = 'failed'))"
	effectResolutionJoin = " LEFT JOIN effect_resolutions r ON r.id = " +
		"(SELECT MAX(id) FROM effect_resolutions WHERE effect_id = e.id)"
)

type ProposalFlag struct {
	Blocked      bool
	BlockedError string
	BlockedUndo  bool
	Undoable     bool
}

func ProposalFlags(ctx context.Context, q db.Querier, ids []int64) (map[int64]ProposalFlag, error) {
	out := map[int64]ProposalFlag{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	list := placeholders(len(ids))
	rows, err := q.QueryContext(ctx,
		"SELECT e.proposal_id, MAX(CASE WHEN "+effectBlockedExpr+" THEN 1 ELSE 0 END), "+
			"MAX(CASE WHEN e.direction = 'undo' AND e.state IN ('unknown','partial') AND r.id IS NULL THEN 1 ELSE 0 END), "+
			"MAX(CASE WHEN "+effectUndoableExpr+" THEN 1 ELSE 0 END) "+
			"FROM effects e"+effectResolutionJoin+" WHERE e.proposal_id IN ("+list+") GROUP BY e.proposal_id", args...)
	if err != nil {
		return nil, err
	}
	if _, err := db.Collect(rows, func(r *sql.Rows) (struct{}, error) {
		var id int64
		var blocked, blockedUndo, undoable int64
		if err := r.Scan(&id, &blocked, &blockedUndo, &undoable); err != nil {
			return struct{}{}, err
		}
		out[id] = ProposalFlag{Blocked: blocked != 0, BlockedUndo: blockedUndo != 0, Undoable: undoable != 0}
		return struct{}{}, nil
	}); err != nil {
		return nil, err
	}
	blockedRows, err := q.QueryContext(ctx,
		"SELECT e.proposal_id, COALESCE(e.error, '') FROM effects e"+effectResolutionJoin+
			" WHERE e.proposal_id IN ("+list+") AND "+effectBlockedExpr+" ORDER BY e.id", args...)
	if err != nil {
		return nil, err
	}
	if _, err := db.Collect(blockedRows, func(r *sql.Rows) (struct{}, error) {
		var id int64
		var text string
		if err := r.Scan(&id, &text); err != nil {
			return struct{}{}, err
		}
		flag := out[id]
		flag.BlockedError = text
		out[id] = flag
		return struct{}{}, nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func BlockedProposalIDs(ctx context.Context, q db.Querier) ([]int64, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT DISTINCT e.proposal_id FROM effects e"+effectResolutionJoin+
			" WHERE e.proposal_id IS NOT NULL AND "+effectBlockedExpr+" ORDER BY e.proposal_id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
}

func UndoableProposalIDs(ctx context.Context, q db.Querier) ([]int64, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT DISTINCT e.proposal_id FROM effects e"+effectResolutionJoin+
			" WHERE e.proposal_id IS NOT NULL AND "+effectUndoableExpr+" ORDER BY e.proposal_id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
}

func ProposalBlocked(ctx context.Context, q db.Querier, id int64) (bool, error) {
	var blocked bool
	err := q.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM effects e"+effectResolutionJoin+
			" WHERE e.proposal_id = ? AND "+effectBlockedExpr+")", id).Scan(&blocked)
	return blocked, err
}

func ProposalsWithStaleIntent(ctx context.Context, q db.Querier, before float64) ([]int64, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT DISTINCT e.proposal_id FROM effects e WHERE e.proposal_id IS NOT NULL AND e.state = 'intent' AND e.lease_at < ? "+
			"AND NOT EXISTS (SELECT 1 FROM effects f WHERE f.proposal_id = e.proposal_id AND f.lease_at >= ?) ORDER BY e.proposal_id",
		before, before)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
}

func ProposalsWithActiveEffects(ctx context.Context, q db.Querier, leaseAfter float64) ([]int64, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT DISTINCT proposal_id FROM effects WHERE proposal_id IS NOT NULL "+
			"AND (state IN ('intent','unknown','partial') OR lease_at >= ?) ORDER BY proposal_id", leaseAfter)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
}

type ProposalAudit struct {
	ID         int64
	TS         float64
	Actor      string
	Source     string
	Outcome    string
	ProposalID int64
}

func ProposalAuditsSince(ctx context.Context, q db.Querier, source string, limit int) ([]ProposalAudit, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, ts, actor, source, outcome, proposal_id FROM effect_resolutions "+
			"WHERE effect_id IS NULL AND proposal_id IS NOT NULL AND source = ? ORDER BY ts DESC, id DESC LIMIT ?", source, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ProposalAudit, error) {
		var a ProposalAudit
		err := r.Scan(&a.ID, &a.TS, &a.Actor, &a.Source, &a.Outcome, &a.ProposalID)
		return a, err
	})
}

func BlockedTarget(ctx context.Context, q db.Querier, prefix string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx,
		"SELECT id FROM effects e WHERE e.direction = 'forward' AND e.state IN ('intent','unknown','partial') "+
			"AND substr(e.target, 1, length(?)) = ? "+
			"AND NOT EXISTS (SELECT 1 FROM effect_resolutions r WHERE r.effect_id = e.id AND r.effective_outcome IS NOT NULL) "+
			"ORDER BY e.id LIMIT 1", prefix, prefix).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func BlockedCount(ctx context.Context, q db.Querier, operation, target string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM effects e WHERE e.direction = 'forward' AND e.operation = ? AND e.target = ? "+
			"AND e.state IN ('intent','unknown','partial') "+
			"AND NOT EXISTS (SELECT 1 FROM effect_resolutions r WHERE r.effect_id = e.id AND r.effective_outcome IS NOT NULL)",
		operation, target).Scan(&n)
	return n, err
}

func CountSince(ctx context.Context, q db.Querier, operation string, since float64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM effects WHERE direction = 'forward' AND operation = ? AND created_at >= ?", operation, since).Scan(&n)
	return n, err
}

type HistoryRow struct {
	ID       int64
	TS       float64
	Tool     string
	UndoneAt *float64
}

func History(ctx context.Context, q db.Querier, target string) ([]HistoryRow, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT e.id, e.created_at, e.operation, (SELECT u.settled_at FROM effects u WHERE u.id = e.undone_by) "+
			"FROM effects e WHERE e.direction = 'forward' AND e.target = ? ORDER BY e.id DESC", target)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (HistoryRow, error) {
		var h HistoryRow
		var undone sql.NullFloat64
		if err := r.Scan(&h.ID, &h.TS, &h.Tool, &undone); err != nil {
			return h, err
		}
		if undone.Valid {
			h.UndoneAt = &undone.Float64
		}
		return h, nil
	})
}

type UndoneEffect struct {
	Operation string
	Target    string
	ThreadID  int64
}

func UndoneSince(ctx context.Context, q db.Querier, from float64, limit int) ([]UndoneEffect, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT e.operation, e.target, COALESCE(e.thread_id, 0) FROM effects e "+
			"JOIN effects u ON u.id = e.undone_by "+
			"WHERE u.settled_at >= ? ORDER BY e.id DESC LIMIT ?", from, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (UndoneEffect, error) {
		var u UndoneEffect
		err := r.Scan(&u.Operation, &u.Target, &u.ThreadID)
		return u, err
	})
}

func PurgeTarget(ctx context.Context, q db.Querier, target string) error {
	_, err := q.ExecContext(ctx,
		"UPDATE effects SET before_json = '{\"purged\":true}', after_json = '{\"purged\":true}', undo_json = '{\"purged\":true}' "+
			"WHERE target = ? AND state <> 'intent'", target)
	return err
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
