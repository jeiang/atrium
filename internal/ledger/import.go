package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

type ImportReport struct {
	Auto                int64    `json:"auto_actions"`
	Agent               int64    `json:"agent_writes"`
	Proposal            int64    `json:"proposal_actions"`
	Resolutions         int64    `json:"write_resolutions"`
	SyntheticUndo       int64    `json:"synthetic_undos"`
	SyntheticResolution int64    `json:"synthetic_resolutions"`
	OrphanAudits        int64    `json:"orphan_audits"`
	DuplicateHolders    int64    `json:"duplicate_holders"`
	Duplicates          []string `json:"possible_duplicates,omitempty"`
	Conservative        []string `json:"conservative_claims,omitempty"`
	MissingIdentity     []string `json:"missing_identity,omitempty"`
}

type LegacyImporter struct {
	ctx  context.Context
	src  db.Querier
	dst  db.Querier
	now  float64
	rep  ImportReport
	used map[int64]bool
	res  map[int64]bool

	auto      map[int64]int64
	autoUndo  map[int64]int64
	agent     map[int64]int64
	agentUndo map[int64]int64
	proposal  map[int64]int64
	direction map[int64]string
	refs      []importRef
	claims    []claimSpec
	synthetic []syntheticSpec
}

type syntheticSpec struct {
	effectID   int64
	proposalID *int64
	outcome    string
	effective  string
	ts         float64
	priorError string
	table      string
	sourceID   int64
}

type importRef struct {
	table        string
	id           int64
	role         string
	effectID     *int64
	resolutionID *int64
}

type claimSpec struct {
	key     string
	effect  int64
	retain  bool
	missing bool
}

func NewLegacyImporter(ctx context.Context, src, dst db.Querier, now float64) *LegacyImporter {
	return &LegacyImporter{
		ctx: ctx, src: src, dst: dst, now: now,
		used: map[int64]bool{}, res: map[int64]bool{},
		auto: map[int64]int64{}, autoUndo: map[int64]int64{}, agent: map[int64]int64{}, agentUndo: map[int64]int64{},
		proposal: map[int64]int64{}, direction: map[int64]string{},
	}
}

func ImportLegacy(ctx context.Context, src, dst db.Querier, now float64) (ImportReport, error) {
	im := NewLegacyImporter(ctx, src, dst, now)
	for _, step := range []func() error{im.AutoActions, im.AgentWrites, im.ProposalActions, im.WriteResolutions, im.Finish} {
		if err := step(); err != nil {
			return im.rep, err
		}
	}
	return im.rep, nil
}

func (im *LegacyImporter) AutoActions() error {
	rows, err := im.src.QueryContext(im.ctx,
		"SELECT id, ts, account, message_id, digest, kind, src, dest, before, after, undo, decision_id, state, error, "+
			"undone_ts, undone_error, undo_started_at FROM auto_actions ORDER BY id")
	if err != nil {
		return err
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (legacyAuto, error) {
		var a legacyAuto
		var src, dest, before, after, undo, errorText, undoneError sql.NullString
		var decisionID sql.NullInt64
		var undoneTS, undoStarted sql.NullFloat64
		err := r.Scan(&a.ID, &a.TS, &a.Account, &a.MessageID, &a.Digest, &a.Kind, &src, &dest, &before, &after, &undo,
			&decisionID, &a.State, &errorText, &undoneTS, &undoneError, &undoStarted)
		a.Src, a.Dest, a.Before, a.After, a.Undo = src, dest, before, after, undo
		a.DecisionID, a.Error, a.UndoneTS, a.UndoneError, a.UndoStartedAt = decisionID, errorText, undoneTS, undoneError, undoStarted
		return a, err
	})
	if err != nil {
		return err
	}
	for _, a := range all {
		if err := im.autoAction(a); err != nil {
			return fmt.Errorf("auto_actions %d: %w", a.ID, err)
		}
		im.rep.Auto++
	}
	return nil
}

type legacyAuto struct {
	ID            int64
	TS            float64
	Account       string
	MessageID     int64
	Digest        string
	Kind          string
	Src           sql.NullString
	Dest          sql.NullString
	Before        sql.NullString
	After         sql.NullString
	Undo          sql.NullString
	DecisionID    sql.NullInt64
	State         string
	Error         sql.NullString
	UndoneTS      sql.NullFloat64
	UndoneError   sql.NullString
	UndoStartedAt sql.NullFloat64
}

func (im *LegacyImporter) autoAction(a legacyAuto) error {
	state, ok := autoForwardState(a.State)
	if !ok {
		return fmt.Errorf("unknown state %q", a.State)
	}
	if a.UndoneTS.Valid && (state == Failed || state == Skipped || state == Partial || state == Unknown) {
		im.rep.Duplicates = append(im.rep.Duplicates, fmt.Sprintf("auto_actions %d has both state %q and undone_ts", a.ID, a.State))
	}
	id := im.effectID(a.ID)
	im.auto[a.ID] = id
	im.direction[id] = "forward"
	legacy := a.json()
	target := fmt.Sprintf("mail:%s:%d", a.Account, a.MessageID)
	if err := im.insertEffect(effectRow{
		id: id, createdAt: a.TS, leaseAt: a.TS, origin: "mail_auto", operation: a.Kind,
		operationKey: fmt.Sprintf("auto_action:%d", a.ID), target: target, direction: "forward",
		account: a.Account, messageID: &a.MessageID, digest: a.Digest, decisionID: intPtr(a.DecisionID),
		src: a.Src.String, dest: a.Dest.String, action: "{}", before: a.Before.String, after: a.After.String,
		undo: a.Undo.String, state: state, error: a.Error.String, legacy: legacy,
	}); err != nil {
		return err
	}
	im.refs = append(im.refs, importRef{table: "auto_actions", id: a.ID, role: "forward", effectID: &id})

	undoState, undo := autoUndoState(a.State)
	if !undo && a.UndoneTS.Valid {
		undoState, undo = Done, true
	}
	if undo {
		undoID := im.effectID(0)
		im.autoUndo[a.ID] = undoID
		im.direction[undoID] = "undo"
		created := a.TS
		if a.UndoStartedAt.Valid {
			created = a.UndoStartedAt.Float64
		} else if a.UndoneTS.Valid {
			created = a.UndoneTS.Float64
		}
		if err := im.insertEffect(effectRow{
			id: undoID, createdAt: created, leaseAt: created, origin: "mail_auto", operation: "legacy_auto_undo",
			operationKey: fmt.Sprintf("auto_action:%d", a.ID), target: target, direction: "undo", undoOf: &id,
			account: a.Account, messageID: &a.MessageID, digest: a.Digest, src: a.Dest.String, dest: a.Src.String,
			action: "{}", before: a.After.String, state: undoState, error: a.UndoneError.String, legacy: legacy,
		}); err != nil {
			return err
		}
		im.rep.SyntheticUndo++
		im.refs = append(im.refs, importRef{table: "auto_actions", id: a.ID, role: "undo", effectID: &undoID})
		if undoState == Done {
			if _, err := im.dst.ExecContext(im.ctx, "UPDATE effects SET undone_by = ? WHERE id = ?", undoID, id); err != nil {
				return err
			}
		}
		if a.State == "undo_settled" {
			if err := im.syntheticResolution(undoID, nil, "not-undone", "failed", a.TS, a.UndoneError.String, "auto_actions", a.ID); err != nil {
				return err
			}
		}
	}
	im.autoClaims(a, id, undoState, undo)
	return nil
}

func autoForwardState(state string) (State, bool) {
	switch state {
	case "held", "interrupted", "unknown":
		return Unknown, true
	case "applied":
		return Done, true
	case "failed":
		return Failed, true
	case "skipped":
		return Skipped, true
	case "partial":
		return Partial, true
	case "undo_held", "undo_unknown", "undo_partial", "undo_failed", "undo_settled", "undone":
		return Done, true
	}
	return "", false
}

func autoUndoState(state string) (State, bool) {
	switch state {
	case "undo_held", "undo_unknown", "undo_settled":
		return Unknown, true
	case "undo_partial":
		return Partial, true
	case "undo_failed":
		return Failed, true
	case "undone":
		return Done, true
	}
	return "", false
}

func (im *LegacyImporter) autoClaims(a legacyAuto, id int64, undoState State, undo bool) {
	released := undo && undoState == Done
	if a.State == "undo_settled" {
		released = true
	}
	if released || a.State == "failed" || a.State == "skipped" {
		return
	}
	key := fmt.Sprintf("auto/%d/%s/%s", a.MessageID, a.Kind, a.Dest.String)
	im.claims = append(im.claims, claimSpec{key: key, effect: id, retain: true})
	if a.Src.Valid && a.Src.String != "" {
		im.claims = append(im.claims, claimSpec{key: fmt.Sprintf("mail_folder/%s/%s", a.Account, a.Src.String), effect: id})
	}
	if a.Dest.Valid && a.Dest.String != "" {
		im.claims = append(im.claims, claimSpec{key: fmt.Sprintf("mail_folder/%s/%s", a.Account, a.Dest.String), effect: id})
	}
	var uidvalidity, uid int64
	var imap string
	err := im.src.QueryRowContext(im.ctx,
		"SELECT m.uidvalidity, m.uid, mb.imap_name FROM messages m JOIN mailboxes mb ON mb.id = m.mailbox_id WHERE m.id = ?",
		a.MessageID).Scan(&uidvalidity, &uid, &imap)
	if err == nil {
		im.claims = append(im.claims, claimSpec{key: fmt.Sprintf("mail/%s/%s/%d/%d", a.Account, imap, uidvalidity, uid), effect: id})
	} else if errors.Is(err, sql.ErrNoRows) {
		im.rep.MissingIdentity = append(im.rep.MissingIdentity, fmt.Sprintf("auto_actions %d: message %d is gone", a.ID, a.MessageID))
		im.claims = append(im.claims, claimSpec{key: fmt.Sprintf("mail_account/%s", a.Account), effect: id, missing: true})
	} else {
		im.rep.MissingIdentity = append(im.rep.MissingIdentity, fmt.Sprintf("auto_actions %d: %v", a.ID, err))
		im.claims = append(im.claims, claimSpec{key: fmt.Sprintf("mail_account/%s", a.Account), effect: id, missing: true})
	}
}

func (im *LegacyImporter) AgentWrites() error {
	rows, err := im.src.QueryContext(im.ctx,
		"SELECT id, ts, thread_id, run_id, tool, target, before_json, after_json, undo_json, undone_at, state, error, resolved_at "+
			"FROM agent_writes ORDER BY id")
	if err != nil {
		return err
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (legacyAgent, error) {
		var a legacyAgent
		var threadID, runID sql.NullInt64
		var before, after, undo, errorText sql.NullString
		var undoneAt, resolvedAt sql.NullFloat64
		err := r.Scan(&a.ID, &a.TS, &threadID, &runID, &a.Tool, &a.Target, &before, &after, &undo, &undoneAt, &a.State, &errorText, &resolvedAt)
		a.ThreadID, a.RunID, a.Before, a.After, a.Undo = threadID, runID, before, after, undo
		a.UndoneAt, a.Error, a.ResolvedAt = undoneAt, errorText, resolvedAt
		return a, err
	})
	if err != nil {
		return err
	}
	for _, a := range all {
		if err := im.agentWrite(a); err != nil {
			return fmt.Errorf("agent_writes %d: %w", a.ID, err)
		}
		im.rep.Agent++
	}
	return nil
}

type legacyAgent struct {
	ID         int64
	TS         float64
	ThreadID   sql.NullInt64
	RunID      sql.NullInt64
	Tool       string
	Target     string
	Before     sql.NullString
	After      sql.NullString
	Undo       sql.NullString
	UndoneAt   sql.NullFloat64
	State      string
	Error      sql.NullString
	ResolvedAt sql.NullFloat64
}

func (im *LegacyImporter) agentWrite(a legacyAgent) error {
	var state State
	switch a.State {
	case "ok":
		state = Done
	case "held", "unknown":
		state = Unknown
	case "resolved":
		state = Unknown
	default:
		return fmt.Errorf("unknown state %q", a.State)
	}
	id := im.effectID(a.ID)
	im.agent[a.ID] = id
	im.direction[id] = "forward"
	legacy := a.json()
	if err := im.insertEffect(effectRow{
		id: id, createdAt: a.TS, leaseAt: a.TS, origin: agentOrigin(a.Tool), operation: a.Tool,
		operationKey: fmt.Sprintf("agent_write:%d", a.ID), target: a.Target, direction: "forward",
		threadID: intPtr(a.ThreadID), action: "{}", before: a.Before.String, after: a.After.String,
		undo: a.Undo.String, state: state, error: a.Error.String, legacy: legacy,
	}); err != nil {
		return err
	}
	im.refs = append(im.refs, importRef{table: "agent_writes", id: a.ID, role: "forward", effectID: &id})
	if a.UndoneAt.Valid && a.State != "ok" {
		im.rep.Duplicates = append(im.rep.Duplicates, fmt.Sprintf("agent_writes %d has state %q and undone_at", a.ID, a.State))
	}
	if a.UndoneAt.Valid && a.State == "ok" {
		undoID := im.effectID(0)
		im.agentUndo[a.ID] = undoID
		im.direction[undoID] = "undo"
		if err := im.insertEffect(effectRow{
			id: undoID, createdAt: a.UndoneAt.Float64, leaseAt: a.UndoneAt.Float64, origin: agentOrigin(a.Tool),
			operation: "legacy_agent_undo", operationKey: fmt.Sprintf("agent_write:%d", a.ID), target: a.Target,
			direction: "undo", undoOf: &id, threadID: intPtr(a.ThreadID), action: "{}",
			before: a.After.String, state: Done, legacy: legacy,
		}); err != nil {
			return err
		}
		im.rep.SyntheticUndo++
		im.refs = append(im.refs, importRef{table: "agent_writes", id: a.ID, role: "undo", effectID: &undoID})
		if _, err := im.dst.ExecContext(im.ctx, "UPDATE effects SET undone_by = ? WHERE id = ?", undoID, id); err != nil {
			return err
		}
	}
	if a.State == "resolved" {
		ts := a.TS
		if a.ResolvedAt.Valid {
			ts = a.ResolvedAt.Float64
		}
		if err := im.syntheticResolution(id, nil, "resolved", "partial", ts, a.Error.String, "agent_writes", a.ID); err != nil {
			return err
		}
	}
	if state == Unknown && a.State != "resolved" {
		im.claims = append(im.claims, claimSpec{key: "legacy_agent/" + a.Target, effect: id, missing: true})
	}
	return nil
}

func agentOrigin(tool string) string {
	switch {
	case strings.HasPrefix(tool, "notion_"):
		return "export"
	case strings.HasPrefix(tool, "tracking_"), strings.HasPrefix(tool, "package_"):
		return "tracking"
	case strings.HasPrefix(tool, "media_"):
		return "media"
	}
	return "agent"
}

func (im *LegacyImporter) ProposalActions() error {
	rows, err := im.src.QueryContext(im.ctx,
		"SELECT id, proposal_id, ts, lease, kind, undo_of, action, undo, state, error FROM proposal_actions ORDER BY id")
	if err != nil {
		return err
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (legacyProposal, error) {
		var p legacyProposal
		var undoOf sql.NullInt64
		var undo, errorText sql.NullString
		err := r.Scan(&p.ID, &p.ProposalID, &p.TS, &p.Lease, &p.Kind, &undoOf, &p.Action, &undo, &p.State, &errorText)
		p.UndoOf, p.Undo, p.Error = undoOf, undo, errorText
		return p, err
	})
	if err != nil {
		return err
	}
	for _, p := range all {
		if p.Kind != "undo" {
			if err := im.proposalAction(p); err != nil {
				return fmt.Errorf("proposal_actions %d: %w", p.ID, err)
			}
		}
	}
	for _, p := range all {
		if p.Kind == "undo" {
			if err := im.proposalAction(p); err != nil {
				return fmt.Errorf("proposal_actions %d: %w", p.ID, err)
			}
		}
	}
	im.rep.Proposal = int64(len(all))
	seen := map[string]int{}
	for _, p := range all {
		if p.Kind == "undo" {
			continue
		}
		key := fmt.Sprintf("proposal %d %s at %g", p.ProposalID, proposalOperation(p.Action), p.TS)
		seen[key]++
	}
	for key, n := range seen {
		if n > 1 {
			im.rep.Duplicates = append(im.rep.Duplicates, fmt.Sprintf("proposal_actions: %d rows share %s", n, key))
		}
	}
	return nil
}

type legacyProposal struct {
	ID         int64
	ProposalID int64
	TS         float64
	Lease      float64
	Kind       string
	UndoOf     sql.NullInt64
	Action     string
	Undo       sql.NullString
	State      string
	Error      sql.NullString
}

func (im *LegacyImporter) proposalAction(p legacyProposal) error {
	operation := proposalOperation(p.Action)
	if p.Kind == "undo" {
		if !p.UndoOf.Valid {
			return errors.New("an undo row has no undo_of")
		}
		forward, ok := im.proposal[p.UndoOf.Int64]
		if !ok {
			return fmt.Errorf("undo_of %d is not a forward proposal action", p.UndoOf.Int64)
		}
		state, ok := proposalUndoState(p.State)
		if !ok {
			return fmt.Errorf("unknown undo state %q", p.State)
		}
		id := im.effectID(p.ID)
		im.proposal[p.ID] = id
		im.direction[id] = "undo"
		legacy := p.json()
		if err := im.insertEffect(effectRow{
			id: id, createdAt: p.TS, leaseAt: p.Lease, origin: "proposal", operation: operation,
			operationKey: fmt.Sprintf("proposal_action:%d", p.ID), target: fmt.Sprintf("proposal:%d", p.ProposalID),
			direction: "undo", undoOf: &forward, proposalID: &p.ProposalID, action: p.Action, undo: p.Undo.String,
			state: state, error: p.Error.String, legacy: legacy,
		}); err != nil {
			return err
		}
		im.refs = append(im.refs, importRef{table: "proposal_actions", id: p.ID, role: "undo", effectID: &id})
		if state == Done {
			if _, err := im.dst.ExecContext(im.ctx, "UPDATE effects SET undone_by = ? WHERE id = ?", id, forward); err != nil {
				return err
			}
		}
		if p.State == "settled" || p.State == "undo_settled" {
			if err := im.syntheticResolution(id, &p.ProposalID, "not-undone", "failed", p.TS, p.Error.String, "proposal_actions", p.ID); err != nil {
				return err
			}
		}
		if state == Unknown || state == Partial {
			im.claims = append(im.claims, claimSpec{key: fmt.Sprintf("proposal/%d/%s", p.ProposalID, operation), effect: id})
		}
		return nil
	}
	state, ok := proposalForwardState(p.State)
	if !ok {
		return fmt.Errorf("unknown state %q", p.State)
	}
	id := im.effectID(p.ID)
	im.proposal[p.ID] = id
	im.direction[id] = "forward"
	legacy := p.json()
	if err := im.insertEffect(effectRow{
		id: id, createdAt: p.TS, leaseAt: p.Lease, origin: "proposal", operation: operation,
		operationKey: fmt.Sprintf("proposal_action:%d", p.ID), target: fmt.Sprintf("proposal:%d", p.ProposalID),
		direction: "forward", proposalID: &p.ProposalID, action: p.Action, undo: p.Undo.String,
		state: state, error: p.Error.String, legacy: legacy,
	}); err != nil {
		return err
	}
	im.refs = append(im.refs, importRef{table: "proposal_actions", id: p.ID, role: "forward", effectID: &id})
	if p.State == "settled" {
		if err := im.syntheticResolution(id, &p.ProposalID, "settled", "partial", p.TS, p.Error.String, "proposal_actions", p.ID); err != nil {
			return err
		}
	}
	if state == Unknown || state == Partial {
		im.claims = append(im.claims, claimSpec{key: fmt.Sprintf("proposal/%d/%s", p.ProposalID, operation), effect: id})
	}
	return nil
}

func proposalForwardState(state string) (State, bool) {
	switch state {
	case "held":
		return Unknown, true
	case "ok", "applied":
		return Done, true
	case "failed":
		return Failed, true
	case "skipped":
		return Skipped, true
	case "partial":
		return Partial, true
	case "unknown", "interrupted":
		return Unknown, true
	case "settled":
		return Unknown, true
	case "undo_held", "undo_failed", "undo_unknown", "undo_partial", "undo_settled", "undone":
		return "", false
	}
	return "", false
}

func proposalUndoState(state string) (State, bool) {
	switch state {
	case "held", "undo_held":
		return Unknown, true
	case "ok", "applied", "undone":
		return Done, true
	case "undo_failed", "failed":
		return Failed, true
	case "undo_unknown", "unknown", "interrupted":
		return Unknown, true
	case "undo_partial", "partial":
		return Partial, true
	case "settled", "undo_settled":
		return Unknown, true
	case "skipped":
		return Skipped, true
	}
	return "", false
}

func proposalOperation(action string) string {
	var value map[string]any
	if json.Unmarshal([]byte(action), &value) == nil {
		for _, key := range []string{"op", "tool"} {
			if text, ok := value[key].(string); ok && text != "" {
				return text
			}
		}
	}
	return "legacy_proposal_action"
}

func (im *LegacyImporter) WriteResolutions() error {
	rows, err := im.src.QueryContext(im.ctx,
		"SELECT id, ts, actor, source, kind, row_id, proposal_id, outcome, prior_error FROM write_resolutions ORDER BY id")
	if err != nil {
		return err
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (legacyResolution, error) {
		var l legacyResolution
		var proposalID sql.NullInt64
		err := r.Scan(&l.ID, &l.TS, &l.Actor, &l.Source, &l.Kind, &l.RowID, &proposalID, &l.Outcome, &l.PriorError)
		l.ProposalID = proposalID
		return l, err
	})
	if err != nil {
		return err
	}
	for _, l := range all {
		if err := im.writeResolution(l); err != nil {
			return fmt.Errorf("write_resolutions %d: %w", l.ID, err)
		}
		im.rep.Resolutions++
	}
	return nil
}

type legacyResolution struct {
	ID         int64
	TS         float64
	Actor      string
	Source     string
	Kind       string
	RowID      int64
	ProposalID sql.NullInt64
	Outcome    string
	PriorError string
}

func (im *LegacyImporter) writeResolution(l legacyResolution) error {
	var effectID, proposalID *int64
	approvalOnly := l.Outcome == "approved" || l.Outcome == "dismissed" || l.Outcome == "suppressed"
	if approvalOnly {
		if l.ProposalID.Valid {
			proposalID = intPtr(l.ProposalID)
		} else {
			proposalID = &l.RowID
		}
	} else {
		switch l.Kind {
		case "auto_action":
			effectID = mapID(im.auto, l.RowID)
		case "auto_undo":
			effectID = mapID(im.autoUndo, l.RowID)
		case "agent_write":
			effectID = mapID(im.agent, l.RowID)
		case "proposal_action":
			effectID = mapID(im.proposal, l.RowID)
		default:
			return fmt.Errorf("unknown kind %q", l.Kind)
		}
		if effectID == nil {
			im.rep.OrphanAudits++
		}
		if l.ProposalID.Valid {
			proposalID = intPtr(l.ProposalID)
		}
	}
	effective := ""
	if !approvalOnly {
		effective = legacyEffective(l.Outcome, effectID, im.direction)
	}
	legacy := l.json()
	id := im.resolutionID(l.ID)
	if _, err := im.dst.ExecContext(im.ctx,
		"INSERT INTO effect_resolutions (id, effect_id, proposal_id, ts, actor, source, outcome, effective_outcome, "+
			"prior_state, prior_error, evidence_json, legacy_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, NULL, ?)",
		id, effectID, proposalID, l.TS, l.Actor, l.Source, l.Outcome, nullText(effective), l.PriorError, legacy); err != nil {
		return err
	}
	im.refs = append(im.refs, importRef{table: "write_resolutions", id: l.ID, role: "resolution", resolutionID: &id})
	return nil
}

func legacyEffective(outcome string, effectID *int64, directions map[int64]string) string {
	switch outcome {
	case "applied", "undone":
		return "done"
	case "not-applied", "not-undone":
		return "failed"
	case "resolved", "settled":
		if effectID != nil && directions[*effectID] == "undo" {
			return "failed"
		}
		return "partial"
	}
	return ""
}

func (im *LegacyImporter) syntheticResolution(effectID int64, proposalID *int64, outcome, effective string, ts float64, priorError, table string, sourceID int64) error {
	im.synthetic = append(im.synthetic, syntheticSpec{
		effectID: effectID, proposalID: proposalID, outcome: outcome, effective: effective,
		ts: ts, priorError: priorError, table: table, sourceID: sourceID,
	})
	return nil
}

func (im *LegacyImporter) insertSynthetic(s syntheticSpec) error {
	var n int64
	if err := im.dst.QueryRowContext(im.ctx,
		"SELECT COUNT(*) FROM effect_resolutions WHERE effect_id = ? AND COALESCE(effective_outcome, '') = ?",
		s.effectID, s.effective).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	legacy := mustJSON(map[string]any{"missing_audit": true, "source_table": s.table, "source_id": s.sourceID})
	res, err := im.dst.ExecContext(im.ctx,
		"INSERT INTO effect_resolutions (effect_id, proposal_id, ts, actor, source, outcome, effective_outcome, "+
			"prior_state, prior_error, evidence_json, legacy_json) VALUES (?, ?, ?, 'import', 'import', ?, ?, NULL, ?, ?, ?)",
		s.effectID, s.proposalID, s.ts, s.outcome, s.effective, s.priorError,
		mustJSON(map[string]any{"missing_audit": true}), legacy)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	im.rep.SyntheticResolution++
	im.refs = append(im.refs, importRef{table: s.table, id: s.sourceID, role: "resolution", resolutionID: &id})
	return nil
}

func (im *LegacyImporter) Finish() error {
	for _, s := range im.synthetic {
		if err := im.insertSynthetic(s); err != nil {
			return err
		}
	}
	for _, ref := range im.refs {
		if _, err := im.dst.ExecContext(im.ctx,
			"INSERT OR REPLACE INTO effect_import_refs (source_table, source_id, role, effect_id, resolution_id) VALUES (?, ?, ?, ?, ?)",
			ref.table, ref.id, ref.role, ref.effectID, ref.resolutionID); err != nil {
			return err
		}
	}
	holders := map[string]map[int64]bool{}
	for _, c := range im.claims {
		if c.missing {
			im.rep.Conservative = append(im.rep.Conservative, c.key)
		}
		if holders[c.key] == nil {
			holders[c.key] = map[int64]bool{}
		}
		holders[c.key][c.effect] = true
	}
	keys := make([]string, 0, len(holders))
	for key := range holders {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if len(holders[key]) > 1 {
			im.rep.DuplicateHolders++
		}
		if _, err := im.dst.ExecContext(im.ctx, "INSERT OR IGNORE INTO effect_claims (resource) VALUES (?)", key); err != nil {
			return err
		}
		for effect := range holders[key] {
			retain := false
			for _, c := range im.claims {
				if c.key == key && c.effect == effect && c.retain {
					retain = true
				}
			}
			if _, err := im.dst.ExecContext(im.ctx,
				"INSERT OR IGNORE INTO effect_resources (effect_id, resource, holding, retain_on_done) VALUES (?, ?, 1, ?)",
				effect, key, boolInt(retain)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a legacyAuto) json() string {
	return mustJSON(map[string]any{
		"id": a.ID, "ts": a.TS, "account": a.Account, "message_id": a.MessageID, "digest": a.Digest, "kind": a.Kind,
		"src": nullString(a.Src), "dest": nullString(a.Dest), "before": nullString(a.Before), "after": nullString(a.After),
		"undo": nullString(a.Undo), "decision_id": nullInt(a.DecisionID), "state": a.State, "error": nullString(a.Error),
		"undone_ts": nullFloat(a.UndoneTS), "undone_error": nullString(a.UndoneError), "undo_started_at": nullFloat(a.UndoStartedAt),
	})
}

func (a legacyAgent) json() string {
	return mustJSON(map[string]any{
		"id": a.ID, "ts": a.TS, "thread_id": nullInt(a.ThreadID), "run_id": nullInt(a.RunID), "tool": a.Tool,
		"target": a.Target, "before_json": nullString(a.Before), "after_json": nullString(a.After),
		"undo_json": nullString(a.Undo), "undone_at": nullFloat(a.UndoneAt), "state": a.State,
		"error": nullString(a.Error), "resolved_at": nullFloat(a.ResolvedAt),
	})
}

func (p legacyProposal) json() string {
	return mustJSON(map[string]any{
		"id": p.ID, "proposal_id": p.ProposalID, "ts": p.TS, "lease": p.Lease, "kind": p.Kind,
		"undo_of": nullInt(p.UndoOf), "action": p.Action, "undo": nullString(p.Undo), "state": p.State,
		"error": nullString(p.Error),
	})
}

func (l legacyResolution) json() string {
	return mustJSON(map[string]any{
		"id": l.ID, "ts": l.TS, "actor": l.Actor, "source": l.Source, "kind": l.Kind, "row_id": l.RowID,
		"proposal_id": nullInt(l.ProposalID), "outcome": l.Outcome, "prior_error": l.PriorError,
	})
}

func nullString(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

func nullInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

func nullFloat(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}

type effectRow struct {
	id           int64
	createdAt    float64
	leaseAt      float64
	origin       string
	operation    string
	operationKey string
	target       string
	direction    string
	undoOf       *int64
	proposalID   *int64
	threadID     *int64
	runID        *int64
	account      string
	messageID    *int64
	digest       string
	decisionID   *int64
	src          string
	dest         string
	action       string
	before       string
	after        string
	undo         string
	state        State
	error        string
	legacy       string
}

func (im *LegacyImporter) insertEffect(e effectRow) error {
	token, err := newToken()
	if err != nil {
		return err
	}
	settled := any(nil)
	if e.state != Intent {
		settled = e.createdAt
	}
	_, err = im.dst.ExecContext(im.ctx, insertEffect,
		e.createdAt, e.leaseAt, token, e.origin, e.operation, e.operationKey, e.direction, e.undoOf,
		e.proposalID, e.threadID, e.runID, nullText(e.account), e.messageID, e.digest, e.decisionID,
		nullText(e.src), nullText(e.dest), e.target, actionText(json.RawMessage(e.action)), nullText(e.before),
		nullText(e.after), nullText(e.undo), string(e.state), settled, nullText(e.error))
	if err != nil {
		return err
	}
	if _, err := im.dst.ExecContext(im.ctx, "UPDATE effects SET legacy_json = ? WHERE id = ?", e.legacy, e.id); err != nil {
		return err
	}
	return nil
}

func (im *LegacyImporter) effectID(preferred int64) int64 {
	if preferred > 0 && !im.used[preferred] {
		im.used[preferred] = true
		return preferred
	}
	max := int64(0)
	for id := range im.used {
		if id > max {
			max = id
		}
	}
	id := max + 1
	for im.used[id] {
		id++
	}
	im.used[id] = true
	return id
}

func (im *LegacyImporter) resolutionID(preferred int64) int64 {
	if preferred > 0 && !im.res[preferred] {
		im.res[preferred] = true
		return preferred
	}
	max := int64(0)
	for id := range im.res {
		if id > max {
			max = id
		}
	}
	id := max + 1
	for im.res[id] {
		id++
	}
	im.res[id] = true
	return id
}

func mapID(m map[int64]int64, id int64) *int64 {
	if v, ok := m[id]; ok {
		return &v
	}
	return nil
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
