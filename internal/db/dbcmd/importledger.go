package dbcmd

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

func ConvertLegacyLedgers(ctx context.Context, src, dst db.Querier, now float64) (ledger.ImportReport, error) {
	auto, err := readLegacyAuto(ctx, src)
	if err != nil {
		return ledger.ImportReport{}, err
	}
	agent, err := readLegacyAgent(ctx, src)
	if err != nil {
		return ledger.ImportReport{}, err
	}
	proposal, err := readLegacyProposal(ctx, src)
	if err != nil {
		return ledger.ImportReport{}, err
	}
	resolutions, err := readLegacyResolutions(ctx, src)
	if err != nil {
		return ledger.ImportReport{}, err
	}
	return ledger.ImportLegacy(ctx, dst, auto, agent, proposal, resolutions, now)
}

func ConvertLegacyAgentWrites(ctx context.Context, src, dst db.Querier, now float64) (ledger.ImportReport, error) {
	agent, err := readLegacyAgent(ctx, src)
	if err != nil {
		return ledger.ImportReport{}, err
	}
	resolutions, err := readLegacyAgentResolutions(ctx, src)
	if err != nil {
		return ledger.ImportReport{}, err
	}
	return ledger.ImportLegacy(ctx, dst, nil, agent, nil, resolutions, now)
}

func readLegacyAuto(ctx context.Context, q db.Querier) ([]ledger.LegacyAutoAction, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, ts, account, message_id, digest, kind, src, dest, before, after, undo, decision_id, state, error, "+
			"undone_ts, undone_error, undo_started_at FROM auto_actions ORDER BY id")
	if err != nil {
		return nil, err
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (ledger.LegacyAutoAction, error) {
		var a ledger.LegacyAutoAction
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
		return nil, err
	}
	for i := range all {
		err := q.QueryRowContext(ctx,
			"SELECT m.uidvalidity, m.uid, mb.imap_name FROM messages m JOIN mailboxes mb ON mb.id = m.mailbox_id WHERE m.id = ?",
			all[i].MessageID).Scan(&all[i].UIDValidity, &all[i].UID, &all[i].IMAPName)
		switch {
		case err == nil:
			all[i].Identity = true
		case errors.Is(err, sql.ErrNoRows):
		default:
			return nil, err
		}
	}
	return all, nil
}

func readLegacyAgent(ctx context.Context, q db.Querier) ([]ledger.LegacyAgentWrite, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, ts, thread_id, run_id, tool, target, before_json, after_json, undo_json, undone_at, state, error, resolved_at "+
			"FROM agent_writes ORDER BY id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ledger.LegacyAgentWrite, error) {
		var a ledger.LegacyAgentWrite
		var threadID, runID sql.NullInt64
		var before, after, undo, errorText sql.NullString
		var undoneAt, resolvedAt sql.NullFloat64
		err := r.Scan(&a.ID, &a.TS, &threadID, &runID, &a.Tool, &a.Target, &before, &after, &undo, &undoneAt, &a.State, &errorText, &resolvedAt)
		a.ThreadID, a.RunID, a.Before, a.After, a.Undo = threadID, runID, before, after, undo
		a.UndoneAt, a.Error, a.ResolvedAt = undoneAt, errorText, resolvedAt
		return a, err
	})
}

func readLegacyProposal(ctx context.Context, q db.Querier) ([]ledger.LegacyProposalAction, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, proposal_id, ts, lease, kind, undo_of, action, undo, state, error FROM proposal_actions ORDER BY id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ledger.LegacyProposalAction, error) {
		var p ledger.LegacyProposalAction
		var undoOf sql.NullInt64
		var undo, errorText sql.NullString
		err := r.Scan(&p.ID, &p.ProposalID, &p.TS, &p.Lease, &p.Kind, &undoOf, &p.Action, &undo, &p.State, &errorText)
		p.UndoOf, p.Undo, p.Error = undoOf, undo, errorText
		return p, err
	})
}

func readLegacyResolutions(ctx context.Context, q db.Querier) ([]ledger.LegacyResolution, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, ts, actor, source, kind, row_id, proposal_id, outcome, prior_error FROM write_resolutions ORDER BY id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ledger.LegacyResolution, error) {
		var l ledger.LegacyResolution
		var proposalID sql.NullInt64
		err := r.Scan(&l.ID, &l.TS, &l.Actor, &l.Source, &l.Kind, &l.RowID, &proposalID, &l.Outcome, &l.PriorError)
		l.ProposalID = proposalID
		return l, err
	})
}

func readLegacyAgentResolutions(ctx context.Context, q db.Querier) ([]ledger.LegacyResolution, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, ts, actor, source, kind, row_id, proposal_id, outcome, prior_error FROM write_resolutions WHERE kind = 'agent_write' ORDER BY id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ledger.LegacyResolution, error) {
		var l ledger.LegacyResolution
		var proposalID sql.NullInt64
		err := r.Scan(&l.ID, &l.TS, &l.Actor, &l.Source, &l.Kind, &l.RowID, &proposalID, &l.Outcome, &l.PriorError)
		l.ProposalID = proposalID
		return l, err
	})
}
