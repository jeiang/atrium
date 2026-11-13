package application

import (
	"context"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/report"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
)

const (
	SyncName         application.Name = "mail.sync"
	ShadowReportName application.Name = "mail.shadow-report"
	ReindexName      application.Name = "mail.reindex"
)

type ShadowReportInput struct {
	Since *string
	Now   float64
}
type ReindexResult struct{ Embedded int }

func DescribeShadowReport() application.Definition {
	return application.Describe[ShadowReportInput, string](ShadowReportName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeReindex() application.Definition {
	return application.Describe[struct{}, ReindexResult](ReindexName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func (s *Service) ShadowReport(ctx context.Context, in ShadowReportInput) (application.Read[string], error) {
	var text string
	rev, err := s.snapshot(ctx, ShadowReportName, in, func(q db.Querier) error {
		var err error
		text, err = report.Build(ctx, q, s.env, in.Since, time.Unix(0, int64(in.Now*1e9)))
		return err
	})
	return application.Read[string]{Revision: rev, Data: text}, err
}

func (s *Service) Reindex(ctx context.Context, _ struct{}) (application.Change[ReindexResult], error) {
	n, err := s.reindex(ctx)
	outcome := state.OK
	if err != nil {
		outcome = auditOutcome(err)
	}
	id := s.writeAudit(ctx, ReindexName, "", outcome, nil)
	return application.Change[ReindexResult]{Data: ReindexResult{Embedded: n}, AuditID: id}, err
}

func (s *Service) reindex(ctx context.Context) (int, error) {
	url, err := embed.EmbedURL(ctx, s.db.Read)
	if err != nil {
		return 0, err
	}
	ctx, err = embed.WithModels(ctx, s.db.Read)
	if err != nil {
		return 0, err
	}
	return embed.Reindex(ctx, s.db, url)
}

type SyncInput struct {
	Account  string
	Accounts []string
}

type PassResult struct {
	Synced    []run.AccountSync
	Committed []int64
	Failed    []int64
	Skipped   []int64
	Unknown   []int64
	Partial   []int64
}

func DescribeSync() application.Definition {
	return application.Describe[SyncInput, PassResult](SyncName, application.Command, []application.Authority{application.Operator, application.System}, application.External, true)
}

func (s *Service) WithClassifiers(classifiers run.Classifier) *Service {
	s.classifiers = classifiers
	return s
}

func (s *Service) WithWatchPass(pass *run.Pass, work context.Context) *Service {
	s.pass, s.workContext = pass, work
	return s
}

func (s *Service) Sync(ctx context.Context, in SyncInput) (application.Change[PassResult], error) {
	accounts := in.Accounts
	if accounts == nil {
		var err error
		accounts, err = run.SelectAccounts(ctx, in.Account)
		if err != nil {
			return application.Change[PassResult]{}, err
		}
	}
	manual := s.db == nil
	if manual {
		d, err := run.Connect(ctx, s.env, s.cfg)
		if err != nil {
			return application.Change[PassResult]{}, err
		}
		defer func() { _ = d.Close() }()
		s = New(d, s.cfg, s.env, s.now).WithOpen(s.open).WithClassifiers(s.classifiers)
	}
	var out application.Change[PassResult]
	var passErr error
	perform := func(ctx context.Context) error {
		p := s.pass
		if p == nil {
			var err error
			p, err = run.Prepare(ctx, s.env, s.db, s.cfg, s.open, nil, s.classifiers)
			if err != nil {
				return err
			}
		}
		execute := func() error { out, passErr = s.syncPass(ctx, p, accounts); return passErr }
		if s.pass != nil {
			return execute()
		}
		return passrun.Track(ctx, s.db, passrun.Sync, execute)
	}
	if !manual {
		err := perform(ctx)
		return out, err
	}
	_, err := module.Invoke(ctx, s.db, s.cfg, run.SyncJobKey, 0, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
		passErr = perform(ctx)
		return scheduler.Result{}, passErr
	})
	if err == nil {
		err = passErr
	}
	return out, err
}

func (s *Service) syncPass(ctx context.Context, p *run.Pass, accounts []string) (application.Change[PassResult], error) {
	lock, err := db.ResourceLocks(ctx, s.cfg.DBPath, []string{mail.PassResource}, 0)
	if err != nil {
		return application.Change[PassResult]{AuditID: s.writeAudit(ctx, SyncName, "", auditOutcome(err), nil)}, err
	}
	defer func() { _ = lock.Close() }()
	work := ctx
	if s.workContext != nil {
		work = s.workContext
	}
	work = db.WithHeld(work, lock)
	after, readErr := ledger.LastEffectID(work, s.db.Read)
	if readErr != nil {
		log.Error("reading mail pass effects failed", logx.Err, readErr.Error())
	}
	synced, err := p.Run(work, accounts)
	out := application.Change[PassResult]{Data: PassResult{Synced: synced}}
	if readErr == nil {
		effects, readErr := ledger.AutoPassEffectsSince(context.WithoutCancel(work), s.db.Read, after)
		if readErr != nil {
			log.Error("reading mail pass effects failed", logx.Err, readErr.Error())
		} else {
			for _, effect := range effects {
				out.EffectIDs = append(out.EffectIDs, effect.ID)
				switch effect.State {
				case ledger.Done:
					out.Data.Committed = append(out.Data.Committed, effect.ID)
				case ledger.Failed:
					out.Data.Failed = append(out.Data.Failed, effect.ID)
				case ledger.Skipped:
					out.Data.Skipped = append(out.Data.Skipped, effect.ID)
				case ledger.Partial:
					out.Data.Partial = append(out.Data.Partial, effect.ID)
				case ledger.Unknown, ledger.Intent:
					out.Data.Unknown = append(out.Data.Unknown, effect.ID)
				}
			}
		}
	}
	outcome := state.OK
	if err != nil {
		outcome = auditOutcome(err)
	}
	switch {
	case len(out.Data.Unknown) > 0:
		outcome = state.Unknown
	case len(out.Data.Partial) > 0 || len(out.Data.Committed) > 0 && (err != nil || len(out.Data.Failed) > 0):
		outcome = state.Partial
	case len(out.Data.Failed) > 0:
		outcome = state.Failed
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		outcome = state.Canceled
	}
	out.AuditID = s.writeAudit(ctx, SyncName, "", outcome, out.EffectIDs)
	return out, err
}
