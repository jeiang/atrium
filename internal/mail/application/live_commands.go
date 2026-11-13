package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/backlog"
	"github.com/jeiang/atrium/internal/mail/live"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
)

const (
	BacklogName        application.Name = "mail.backlog"
	RebaselineName     application.Name = "mail.rebaseline"
	ApplyDeletionsName application.Name = "mail.apply-deletions"
)

type RebaselineResult struct {
	Baseline any              `json:"baseline"`
	Created  []int64          `json:"created"`
	Accounts backlog.Accounts `json:"accounts"`
}
type ApplyDeletionsInput struct{ Accounts []string }
type AccountDeletions struct {
	Account string
	Ran     bool
	Trashed int
	Skipped int
	Failed  int
}
type ApplyDeletionsResult struct{ Accounts []AccountDeletions }

func DescribeBacklog() application.Definition {
	return application.Describe[struct{}, backlog.Result](BacklogName, application.Command, []application.Authority{application.Operator}, application.NoEffect, false)
}
func DescribeRebaseline() application.Definition {
	return application.Describe[struct{}, RebaselineResult](RebaselineName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeApplyDeletions() application.Definition {
	return application.Describe[ApplyDeletionsInput, ApplyDeletionsResult](ApplyDeletionsName, application.Command, []application.Authority{application.Operator}, application.External, true)
}

func (s *Service) WithSyncer(syncer *mailsync.Syncer) *Service { s.syncer = syncer; return s }
func (s *Service) WithSync(sync func(context.Context) error) *Service {
	s.syncAll = sync
	return s
}

func (s *Service) Backlog(ctx context.Context, _ struct{}) (application.Change[backlog.Result], error) {
	result, err := backlog.Backlog(ctx, s.db, s.now())
	return application.Change[backlog.Result]{Data: result}, err
}

func (s *Service) Rebaseline(ctx context.Context, _ struct{}) (application.Change[RebaselineResult], error) {
	out, err := s.rebaseline(ctx)
	outcome := state.OK
	if err != nil {
		outcome = auditOutcome(err)
	}
	auditID := s.writeAudit(ctx, RebaselineName, "", outcome, nil)
	return application.Change[RebaselineResult]{Data: out, AuditID: auditID}, err
}

func (s *Service) rebaseline(ctx context.Context) (RebaselineResult, error) {
	if err := s.syncAll(ctx); err != nil {
		return RebaselineResult{}, err
	}
	baseline, err := s.live.Rebaseline(ctx)
	if err != nil {
		return RebaselineResult{}, err
	}
	refreshed, err := backlog.Refresh(ctx, s.db, s.now())
	if err != nil {
		return RebaselineResult{}, err
	}
	return RebaselineResult{Baseline: baseline, Created: refreshed.Created, Accounts: refreshed.Accounts}, nil
}

func (s *Service) ApplyDeletions(ctx context.Context, in ApplyDeletionsInput) (application.Change[ApplyDeletionsResult], error) {
	var out ApplyDeletionsResult
	var err error
	var written, failed int
	for _, account := range in.Accounts {
		var done applier.Outcome
		done, err = applier.New(s.live, s.open, s.syncer).DeletionPass(ctx, account, true)
		written += done.Written
		failed += done.Failed
		if err != nil {
			break
		}
		out.Accounts = append(out.Accounts, AccountDeletions{Account: account, Ran: done.Ran, Trashed: done.Kind(live.Trash), Skipped: done.Skipped, Failed: done.Failed})
	}
	outcome := state.OK
	if err != nil {
		outcome = auditOutcome(err)
	}
	if written > 0 && (err != nil || failed > 0) {
		outcome = state.Partial
	} else if failed > 0 {
		outcome = state.Failed
	}
	auditID := s.writeAudit(ctx, ApplyDeletionsName, "", outcome, nil)
	if err != nil {
		return application.Change[ApplyDeletionsResult]{AuditID: auditID}, err
	}
	return application.Change[ApplyDeletionsResult]{Data: out, AuditID: auditID}, nil
}
