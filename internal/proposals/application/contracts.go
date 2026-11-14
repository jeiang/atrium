package application

import (
	"errors"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
)

type (
	Proposal       = proposals.Proposal
	ProposalView   = proposals.ProposalView
	Definition     = proposals.Definition
	Kind           = proposals.Kind
	Builder        = proposals.Builder
	UndoResult     = proposals.UndoResult
	SelectionError = proposals.SelectionError
	ProposalError  = proposals.ProposalError
)

const (
	Pending           = proposals.Pending
	OutcomeUndone     = proposals.OutcomeUndone
	OutcomeNotUndone  = proposals.OutcomeNotUndone
	OutcomeApplied    = proposals.OutcomeApplied
	OutcomeNotApplied = proposals.OutcomeNotApplied
)

var (
	Statuses = proposals.Statuses
	Outcomes = proposals.Outcomes
)

func IsLockBusy(err error) bool {
	return errors.As(err, new(*db.LockBusy))
}
