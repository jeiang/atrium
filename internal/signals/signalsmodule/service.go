package signalsmodule

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	commitmentsapp "github.com/jeiang/atrium/internal/commitments/application"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/signals"
)

var log = logx.Logger("atrium.signals")

const (
	OpenLoopsName            application.Name = "mail.open-loops"
	CommitmentsName          application.Name = "mail.commitments"
	LoopSnoozeName           application.Name = "mail.loop-snooze"
	LoopDismissName          application.Name = "mail.loop-dismiss"
	LoopsDismissMatchingName application.Name = "mail.loops-dismiss-matching"
	CommitmentDoneName       application.Name = "mail.commitment-done"
	CommitmentDismissName    application.Name = "mail.commitment-dismiss"
)

type OpenLoopsInput struct {
	State  string
	Limit  int
	Snooze bool
}
type OpenLoopsResult struct {
	Loops         []signals.Loop `json:"loops"`
	SnoozeDefault int            `json:"snooze_default"`
}
type CommitmentsInput struct {
	Status string
	Limit  int
}
type CommitmentsResult struct {
	Commitments []signals.Commitment `json:"commitments"`
}
type LoopSnoozeInput struct {
	ID   int64
	Days *int64
}
type LoopSnoozeResult struct{ Days int64 }
type LoopInput struct{ ID int64 }
type LoopsDismissMatchingInput struct{ State string }
type LoopsDismissMatchingResult struct{ Count int64 }
type CommitmentInput struct{ ID int64 }

func DescribeOpenLoops() application.Definition {
	return application.Describe[OpenLoopsInput, OpenLoopsResult](OpenLoopsName, application.Query, []application.Authority{application.Operator, application.Agent}, application.NoEffect, false)
}
func DescribeCommitments() application.Definition {
	return application.Describe[CommitmentsInput, CommitmentsResult](CommitmentsName, application.Query, []application.Authority{application.Operator, application.Agent}, application.NoEffect, false)
}
func DescribeLoopSnooze() application.Definition {
	return application.Describe[LoopSnoozeInput, LoopSnoozeResult](LoopSnoozeName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeLoopDismiss() application.Definition {
	return application.Describe[LoopInput, struct{}](LoopDismissName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeLoopsDismissMatching() application.Definition {
	return application.Describe[LoopsDismissMatchingInput, LoopsDismissMatchingResult](LoopsDismissMatchingName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeCommitmentDone() application.Definition {
	return application.Describe[CommitmentInput, struct{}](CommitmentDoneName, application.Command, []application.Authority{application.Operator}, application.LocalUndoable, true)
}
func DescribeCommitmentDismiss() application.Definition {
	return application.Describe[CommitmentInput, struct{}](CommitmentDismissName, application.Command, []application.Authority{application.Operator}, application.LocalUndoable, true)
}

func Describe() []application.Definition {
	return []application.Definition{DescribeOpenLoops(), DescribeCommitments(), DescribeLoopSnooze(), DescribeLoopDismiss(),
		DescribeLoopsDismissMatching(), DescribeCommitmentDone(), DescribeCommitmentDismiss()}
}

func WallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

type Service struct {
	db          *db.DB
	state       *state.Store
	now         func() float64
	commitments *commitmentsapp.Service
}

func New(d *db.DB, now func() float64, commitments *commitmentsapp.Service) *Service {
	return &Service{db: d, state: state.New(d), now: now, commitments: commitments}
}

func (s *Service) snapshot(ctx context.Context, name application.Name, input any, fn func(db.Querier) error) (application.Revision, error) {
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, []string{state.Mail, state.Effects, state.Review, state.Settings, state.Calendar, state.Connections})
	if err != nil {
		return "", err
	}
	if err = fn(tx); err != nil {
		return "", err
	}
	return application.Revision(state.Revision(string(name), counters, input, "operator")), nil
}

func (s *Service) writeAudit(ctx context.Context, name application.Name, target string, outcome state.Outcome) int64 {
	id, err := s.state.Audit(context.WithoutCancel(ctx), nil, state.Entry{Operation: string(name), Outcome: outcome, Target: target, DetailsJSON: "{}"})
	if err != nil {
		log.Error("operation audit failed", "operation", string(name), logx.Err, err.Error())
	}
	return id
}

func auditOutcome(err error) state.Outcome {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return state.Canceled
	}
	var classified *application.Error
	if !errors.As(err, &classified) {
		return state.Failed
	}
	switch classified.Code {
	case application.Invalid:
		return state.Invalid
	case application.Conflict:
		return state.Conflict
	case application.NotFound:
		return state.NotFound
	case application.Unknown:
		return state.Unknown
	case application.Forbidden, application.Refused, application.NotConfigured:
		return state.Refused
	}
	return state.Failed
}

func ClampDays(raw string) (int64, bool) {
	body := strings.TrimSpace(raw)
	neg := false
	if rest, ok := strings.CutPrefix(body, "-"); ok {
		body, neg = rest, true
	} else if rest, ok := strings.CutPrefix(body, "+"); ok {
		body = rest
	}
	whole, fraction, hasFraction := strings.Cut(body, ".")
	if hasFraction && (fraction == "" || strings.Trim(fraction, "0") != "") {
		return 0, false
	}
	if whole == "" {
		return 0, false
	}
	n, ok := new(big.Int).SetString(strings.ReplaceAll(whole, "_", ""), 10)
	if !ok {
		return 0, false
	}
	if neg {
		n.Neg(n)
	}
	switch {
	case n.Cmp(big.NewInt(1)) < 0:
		return 1, true
	case n.Cmp(big.NewInt(60)) > 0:
		return 60, true
	}
	return n.Int64(), true
}

func notFound(what string) error {
	return &application.Error{Code: application.NotFound, Message: "unknown " + what}
}

func (s *Service) localChange(ctx context.Context, name application.Name, target string, what string, fn func() (bool, error)) (int64, error) {
	found, err := fn()
	if err == nil && !found {
		err = notFound(what)
	}
	outcome := state.OK
	if err != nil {
		outcome = auditOutcome(err)
	}
	return s.writeAudit(ctx, name, target, outcome), err
}

func (s *Service) OpenLoops(ctx context.Context, in OpenLoopsInput) (application.Read[OpenLoopsResult], error) {
	var data OpenLoopsResult
	now := s.now()
	rev, err := s.snapshot(ctx, OpenLoopsName, in, func(q db.Querier) error {
		loops, err := signals.ListLoops(ctx, q, now, in.State, in.Limit, false)
		if err != nil {
			return err
		}
		data.Loops = loops
		if !in.Snooze {
			return nil
		}
		data.SnoozeDefault, err = signals.SnoozeDefault(ctx, q)
		return err
	})
	return application.Read[OpenLoopsResult]{Revision: rev, Data: data}, err
}

func (s *Service) Commitments(ctx context.Context, in CommitmentsInput) (application.Read[CommitmentsResult], error) {
	var data CommitmentsResult
	rev, err := s.snapshot(ctx, CommitmentsName, in, func(q db.Querier) error {
		var err error
		data.Commitments, err = signals.ListCommitments(ctx, q, in.Status, in.Limit)
		return err
	})
	return application.Read[CommitmentsResult]{Revision: rev, Data: data}, err
}

func (s *Service) LoopSnooze(ctx context.Context, in LoopSnoozeInput) (application.Change[LoopSnoozeResult], error) {
	var days int64
	if in.Days != nil {
		days = max(1, min(*in.Days, 60))
	} else {
		snooze, err := signals.SnoozeDefault(ctx, s.db.Read)
		if err != nil {
			return application.Change[LoopSnoozeResult]{}, err
		}
		days = int64(snooze)
	}
	now := s.now()
	auditID, err := s.localChange(ctx, LoopSnoozeName, strconv.FormatInt(in.ID, 10), "loop", func() (bool, error) {
		return signals.Snooze(ctx, s.db.Write, in.ID, now+float64(days)*signals.Day, now)
	})
	return application.Change[LoopSnoozeResult]{Data: LoopSnoozeResult{Days: days}, AuditID: auditID}, err
}

func (s *Service) LoopDismiss(ctx context.Context, in LoopInput) (application.Change[struct{}], error) {
	auditID, err := s.localChange(ctx, LoopDismissName, strconv.FormatInt(in.ID, 10), "loop", func() (bool, error) {
		return signals.Dismiss(ctx, s.db.Write, in.ID, s.now())
	})
	return application.Change[struct{}]{AuditID: auditID}, err
}

func (s *Service) LoopsDismissMatching(ctx context.Context, in LoopsDismissMatchingInput) (application.Change[LoopsDismissMatchingResult], error) {
	if in.State != signals.OweReply && in.State != signals.Waiting {
		s.writeAudit(ctx, LoopsDismissMatchingName, in.State, state.Invalid)
		return application.Change[LoopsDismissMatchingResult]{}, &application.Error{Code: application.Invalid, Message: "unknown state"}
	}
	var count int64
	auditID, err := s.localChange(ctx, LoopsDismissMatchingName, in.State, "state", func() (bool, error) {
		var err error
		count, err = signals.DismissAll(ctx, s.db.Write, in.State, s.now())
		return true, err
	})
	return application.Change[LoopsDismissMatchingResult]{Data: LoopsDismissMatchingResult{Count: count}, AuditID: auditID}, err
}

func commitmentChange(c application.Change[commitmentsapp.Changed], err error) (application.Change[struct{}], error) {
	return application.Change[struct{}]{EffectIDs: c.EffectIDs, AuditID: c.AuditID}, err
}

func commitmentRef(id int64) commitmentsapp.RefInput {
	return commitmentsapp.RefInput{Ref: commitmentsapp.SourceCommitment + ":" + strconv.FormatInt(id, 10)}
}

func (s *Service) CommitmentDone(ctx context.Context, in CommitmentInput) (application.Change[struct{}], error) {
	return commitmentChange(s.commitments.DoneAs(ctx, CommitmentDoneName, commitmentRef(in.ID)))
}

func (s *Service) CommitmentDismiss(ctx context.Context, in CommitmentInput) (application.Change[struct{}], error) {
	return commitmentChange(s.commitments.DismissAs(ctx, CommitmentDismissName, commitmentRef(in.ID)))
}
