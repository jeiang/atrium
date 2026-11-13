package application

import (
	"context"
	"math/big"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/watches"
)

const (
	OpenLoopsName            application.Name = "mail.open-loops"
	CommitmentsName          application.Name = "mail.commitments"
	LoopSnoozeName           application.Name = "mail.loop-snooze"
	LoopDismissName          application.Name = "mail.loop-dismiss"
	LoopsDismissMatchingName application.Name = "mail.loops-dismiss-matching"
	CommitmentDoneName       application.Name = "mail.commitment-done"
	CommitmentDismissName    application.Name = "mail.commitment-dismiss"
	RefreshLoopsName         application.Name = "mail.refresh-loops"
	RefreshWatchesName       application.Name = "mail.refresh-watches"
)

type OpenLoopsInput struct {
	State  string
	Limit  int
	Snooze bool
}
type OpenLoopsResult struct {
	Loops         []openloops.Loop `json:"loops"`
	SnoozeDefault int              `json:"snooze_default"`
}
type CommitmentsInput struct {
	Status string
	Limit  int
}
type CommitmentsResult struct {
	Commitments []openloops.Commitment `json:"commitments"`
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
type RefreshResult struct{ Result any }

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
	return application.Describe[CommitmentInput, struct{}](CommitmentDoneName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeCommitmentDismiss() application.Definition {
	return application.Describe[CommitmentInput, struct{}](CommitmentDismissName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeRefreshLoops() application.Definition {
	return application.Describe[struct{}, RefreshResult](RefreshLoopsName, application.Command, []application.Authority{application.Operator}, application.Conditional, false)
}
func DescribeRefreshWatches() application.Definition {
	return application.Describe[struct{}, RefreshResult](RefreshWatchesName, application.Command, []application.Authority{application.Operator}, application.Conditional, false)
}

func (s *Service) WithClassifier(c *classifier.Service) *Service { s.classifier = c; return s }
func (s *Service) WithClock(now func() float64) *Service         { s.now = now; return s }

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
	return s.writeAudit(ctx, name, target, outcome, nil), err
}

func (s *Service) OpenLoops(ctx context.Context, in OpenLoopsInput) (application.Read[OpenLoopsResult], error) {
	var data OpenLoopsResult
	now := s.now()
	rev, err := s.snapshot(ctx, OpenLoopsName, in, func(q db.Querier) error {
		loops, err := openloops.ListLoops(ctx, q, now, in.State, in.Limit, false)
		if err != nil {
			return err
		}
		data.Loops = loops
		if !in.Snooze {
			return nil
		}
		data.SnoozeDefault, err = openloops.SnoozeDefault(ctx, q)
		return err
	})
	return application.Read[OpenLoopsResult]{Revision: rev, Data: data}, err
}

func (s *Service) Commitments(ctx context.Context, in CommitmentsInput) (application.Read[CommitmentsResult], error) {
	var data CommitmentsResult
	rev, err := s.snapshot(ctx, CommitmentsName, in, func(q db.Querier) error {
		var err error
		data.Commitments, err = openloops.ListCommitments(ctx, q, in.Status, in.Limit)
		return err
	})
	return application.Read[CommitmentsResult]{Revision: rev, Data: data}, err
}

func (s *Service) LoopSnooze(ctx context.Context, in LoopSnoozeInput) (application.Change[LoopSnoozeResult], error) {
	var days int64
	if in.Days != nil {
		days = max(1, min(*in.Days, 60))
	} else {
		snooze, err := openloops.SnoozeDefault(ctx, s.db.Read)
		if err != nil {
			return application.Change[LoopSnoozeResult]{}, err
		}
		days = int64(snooze)
	}
	now := s.now()
	auditID, err := s.localChange(ctx, LoopSnoozeName, strconv.FormatInt(in.ID, 10), "loop", func() (bool, error) {
		return openloops.Snooze(ctx, s.db.Write, in.ID, now+float64(days)*openloops.Day, now)
	})
	return application.Change[LoopSnoozeResult]{Data: LoopSnoozeResult{Days: days}, AuditID: auditID}, err
}

func (s *Service) LoopDismiss(ctx context.Context, in LoopInput) (application.Change[struct{}], error) {
	auditID, err := s.localChange(ctx, LoopDismissName, strconv.FormatInt(in.ID, 10), "loop", func() (bool, error) {
		return openloops.Dismiss(ctx, s.db.Write, in.ID, s.now())
	})
	return application.Change[struct{}]{AuditID: auditID}, err
}

func (s *Service) LoopsDismissMatching(ctx context.Context, in LoopsDismissMatchingInput) (application.Change[LoopsDismissMatchingResult], error) {
	if in.State != openloops.OweReply && in.State != openloops.Waiting {
		s.writeAudit(ctx, LoopsDismissMatchingName, in.State, state.Invalid, nil)
		return application.Change[LoopsDismissMatchingResult]{}, &application.Error{Code: application.Invalid, Message: "unknown state"}
	}
	var count int64
	auditID, err := s.localChange(ctx, LoopsDismissMatchingName, in.State, "state", func() (bool, error) {
		var err error
		count, err = openloops.DismissAll(ctx, s.db.Write, in.State, s.now())
		return true, err
	})
	return application.Change[LoopsDismissMatchingResult]{Data: LoopsDismissMatchingResult{Count: count}, AuditID: auditID}, err
}

func (s *Service) setCommitment(ctx context.Context, name application.Name, id int64, status string) (application.Change[struct{}], error) {
	auditID, err := s.localChange(ctx, name, strconv.FormatInt(id, 10), "commitment", func() (bool, error) {
		return openloops.SetCommitmentStatus(ctx, s.db.Write, id, status)
	})
	return application.Change[struct{}]{AuditID: auditID}, err
}

func (s *Service) CommitmentDone(ctx context.Context, in CommitmentInput) (application.Change[struct{}], error) {
	return s.setCommitment(ctx, CommitmentDoneName, in.ID, openloops.Done)
}

func (s *Service) CommitmentDismiss(ctx context.Context, in CommitmentInput) (application.Change[struct{}], error) {
	return s.setCommitment(ctx, CommitmentDismissName, in.ID, openloops.Dismissed)
}

func (s *Service) RefreshLoops(ctx context.Context, _ struct{}) (application.Change[RefreshResult], error) {
	out, err := openloops.Refresh(ctx, s.db, s.now(), openloops.Options{Env: s.env, Classifier: s.classifier, Clock: s.now, CacheDir: s.cfg.CacheDir})
	if err != nil {
		return application.Change[RefreshResult]{}, err
	}
	return application.Change[RefreshResult]{Data: RefreshResult{Result: out}}, nil
}

func (s *Service) RefreshWatches(ctx context.Context, _ struct{}) (application.Change[RefreshResult], error) {
	out, err := watches.Refresh(ctx, s.db, s.now(), watches.Options{Classifier: s.classifier, Clock: s.now, Env: s.env})
	if err != nil {
		return application.Change[RefreshResult]{}, err
	}
	return application.Change[RefreshResult]{Data: RefreshResult{Result: out}}, nil
}
