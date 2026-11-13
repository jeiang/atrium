package mailmodule

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/backlog"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/mail/replay"
	"github.com/jeiang/atrium/internal/mail/report"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/mail/scanutil"
	"github.com/jeiang/atrium/internal/web"
)

const (
	outcomeOK      = "ok"
	outcomeRefused = "refused"
	outcomeUnknown = "unknown"
	outcomePartial = "partial"
	outcomeFailed  = "failed"
)

type MailScanned struct {
	Outcome    string `json:"outcome" api:"enum=ok"`
	Candidates int    `json:"candidates"`
	Created    int    `json:"created"`
}
type MailBacklog struct {
	Outcome  string          `json:"outcome" api:"enum=ok"`
	Created  []int64         `json:"created"`
	Accounts json.RawMessage `json:"accounts"`
}
type MailRebaselined struct {
	Outcome  string          `json:"outcome" api:"enum=ok"`
	Baseline json.RawMessage `json:"baseline"`
	Created  []int64         `json:"created"`
	Accounts json.RawMessage `json:"accounts"`
}
type MailAccountDeletions struct {
	Account string `json:"account"`
	Ran     bool   `json:"ran"`
	Trashed int    `json:"trashed"`
	Skipped int    `json:"skipped"`
	Failed  int    `json:"failed"`
}
type MailDeletions struct {
	Outcome  string                 `json:"outcome" api:"enum=ok"`
	Accounts []MailAccountDeletions `json:"accounts"`
}
type MailSyncRequest struct {
	Account string `json:"account,omitempty"`
}
type MailAccountSynced struct {
	Account     string  `json:"account"`
	New         int     `json:"new"`
	GoneIDs     []int64 `json:"gone_ids"`
	FlagChanges int     `json:"flag_changes"`
	Mailboxes   int     `json:"mailboxes"`
}
type MailSynced struct {
	Outcome   string              `json:"outcome" api:"enum=ok"`
	Accounts  []MailAccountSynced `json:"accounts"`
	Committed []int64             `json:"committed"`
	Skipped   []int64             `json:"skipped"`
}
type MailReplayRequest struct {
	Account string `json:"account,omitempty"`
	Limit   int    `json:"limit,omitempty" api:"min=0"`
}
type MailReplaySummary struct {
	Messages int            `json:"messages"`
	Stages   map[string]int `json:"stages"`
	Differ   int            `json:"differ"`
}
type MailReplayReason struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
}
type MailReplay struct {
	Lines   []replay.Line      `json:"lines"`
	Summary MailReplaySummary  `json:"summary"`
	Reasons []MailReplayReason `json:"reasons"`
}
type MailShadowReportRequest struct {
	Since *string `query:"since" json:"-"`
}
type MailShadowReport struct {
	Report string `json:"report"`
}
type MailReindexed struct {
	Outcome  string `json:"outcome" api:"enum=ok"`
	Embedded int    `json:"embedded"`
}

func mailOutcome(effects []ledger.AutoEffectOutcome, err error) (string, []int64) {
	var ids []int64
	var done, failed, partial, unknown int
	for _, effect := range effects {
		ids = append(ids, effect.ID)
		switch effect.State {
		case ledger.Done:
			done++
		case ledger.Failed:
			failed++
		case ledger.Partial:
			partial++
		case ledger.Unknown, ledger.Intent:
			unknown++
		}
	}
	switch {
	case unknown > 0:
		return outcomeUnknown, ids
	case partial > 0 || done > 0 && (err != nil || failed > 0):
		return outcomePartial, ids
	case failed > 0:
		return outcomeFailed, ids
	case err != nil:
		return "", ids
	}
	return outcomeOK, ids
}

func outcomeError(outcome, message string, ids []int64, cause error) error {
	code := map[string]application.Code{outcomeRefused: application.Refused, outcomeUnknown: application.Unknown, outcomePartial: application.Partial, outcomeFailed: application.Conflict}[outcome]
	if code == "" {
		return cause
	}
	if message == "" && cause != nil {
		message = cause.Error()
	}
	if message == "" {
		message = "The mail pass finished with outcome " + outcome + "."
	}
	return &application.Error{Code: code, Reason: outcome, Message: message, EffectIDs: ids, Cause: cause}
}

func unknownMailAccount(err error) error {
	var unknown run.UnknownAccount
	if errors.As(err, &unknown) {
		return &application.Error{Code: application.Invalid, Message: err.Error(), Fields: []application.Field{{Name: "account", Code: "invalid", Message: "Unknown mail account"}}, Cause: err}
	}
	return err
}

func rawJSON(v any) (json.RawMessage, error) {
	return json.Marshal(v)
}

func mailPassRoutes(d *web.Deps, m Module) []application.Binding {
	passes := mailPasses{d: d, m: m}
	service := func() *mailapp.Service { return mailPageService(d) }
	const area = "mail"
	refusals := []application.Code{application.Refused}
	effects := []application.Code{application.Refused, application.Unknown, application.Partial, application.Conflict}
	scan := func(def application.Definition, target string, do func(*mailapp.Service, context.Context) (application.Change[scanutil.Result], error)) application.Binding {
		return api.Command(d, def, application.APIShape{Area: area, Errors: refusals}, target, func(ctx context.Context, _ struct{}) (out application.Change[MailScanned], err error) {
			err = passes.locked(ctx, func(ctx context.Context, p *run.Pass) error {
				done, err := do(mailCLIService(d.Env)(p).WithNamer(rebalance.SuggestName(p.Classifier)), ctx)
				out = application.Change[MailScanned]{Data: MailScanned{Outcome: outcomeOK, Candidates: done.Data.Candidates, Created: done.Data.Created}, AuditID: done.AuditID, EffectIDs: done.EffectIDs}
				return err
			})
			return out, err
		})
	}
	return []application.Binding{
		scan(mailapp.DescribeScanRebalance(), "POST /mail/scans/rebalance", func(s *mailapp.Service, ctx context.Context) (application.Change[scanutil.Result], error) {
			return s.ScanRebalance(ctx, mailapp.ScanInput{})
		}),
		scan(mailapp.DescribeScanDeletions(), "POST /mail/scans/deletions", func(s *mailapp.Service, ctx context.Context) (application.Change[scanutil.Result], error) {
			return s.ScanDeletions(ctx, mailapp.ScanInput{})
		}),
		scan(mailapp.DescribeScanGmailLabels(), "POST /mail/scans/gmail-labels", func(s *mailapp.Service, ctx context.Context) (application.Change[scanutil.Result], error) {
			return s.ScanGmailLabels(ctx, mailapp.ScanInput{})
		}),
		api.Command(d, mailapp.DescribeBacklog(), application.APIShape{Area: area, Errors: refusals}, "POST /mail/live/backlog", func(ctx context.Context, _ struct{}) (application.Change[MailBacklog], error) {
			done, err := service().Backlog(ctx, struct{}{})
			if backlogErr := backlog.Error(""); errors.As(err, &backlogErr) {
				return application.Change[MailBacklog]{}, outcomeError(outcomeRefused, err.Error(), nil, err)
			}
			if err != nil {
				return application.Change[MailBacklog]{}, err
			}
			accounts, err := rawJSON(done.Data.Accounts)
			return application.Change[MailBacklog]{Data: MailBacklog{Outcome: outcomeOK, Created: done.Data.Created, Accounts: accounts}, AuditID: done.AuditID}, err
		}),
		api.Command(d, mailapp.DescribeRebaseline(), application.APIShape{Area: area, Errors: refusals}, "POST /mail/live/rebaseline", func(ctx context.Context, _ struct{}) (out application.Change[MailRebaselined], err error) {
			err = passes.locked(ctx, func(ctx context.Context, p *run.Pass) error {
				done, err := mailCLIService(d.Env)(p).WithSync(p.SyncAll).Rebaseline(ctx, struct{}{})
				if err != nil {
					return err
				}
				out = application.Change[MailRebaselined]{Data: MailRebaselined{Outcome: outcomeOK, Created: done.Data.Created}, AuditID: done.AuditID}
				if out.Data.Baseline, err = rawJSON(done.Data.Baseline); err != nil {
					return err
				}
				out.Data.Accounts, err = rawJSON(done.Data.Accounts)
				return err
			})
			return out, err
		}),
		api.Command(d, mailapp.DescribeApplyDeletions(), application.APIShape{Area: area, Errors: effects}, "POST /mail/live/deletions/apply", func(ctx context.Context, _ struct{}) (out application.Change[MailDeletions], err error) {
			err = passes.locked(ctx, func(ctx context.Context, p *run.Pass) error {
				accounts, err := run.SelectAccounts(ctx, "")
				if err != nil {
					return err
				}
				after, err := ledger.LastEffectID(ctx, d.DB.Read)
				if err != nil {
					return err
				}
				done, passErr := mailCLIService(d.Env)(p).WithOpen(p.Open).WithSyncer(p.Syncer).ApplyDeletions(ctx, mailapp.ApplyDeletionsInput{Accounts: accounts})
				written, err := ledger.AutoPassEffectsSince(context.WithoutCancel(ctx), d.DB.Read, after)
				if err != nil {
					return errors.Join(passErr, err)
				}
				outcome, ids := mailOutcome(written, passErr)
				out = application.Change[MailDeletions]{Data: MailDeletions{Outcome: outcome, Accounts: []MailAccountDeletions{}}, AuditID: done.AuditID, EffectIDs: ids}
				ran := false
				for _, a := range done.Data.Accounts {
					ran = ran || a.Ran
					out.Data.Accounts = append(out.Data.Accounts, MailAccountDeletions{Account: a.Account, Ran: a.Ran, Trashed: a.Trashed, Skipped: a.Skipped, Failed: a.Failed})
				}
				if outcome == outcomeOK && !ran && len(accounts) > 0 {
					return outcomeError(outcomeRefused, "No deletion pass ran: live mail writes are in shadow mode or paused.", ids, nil)
				}
				if outcome != outcomeOK {
					return outcomeError(outcome, "", ids, passErr)
				}
				return nil
			})
			return out, err
		}),
		api.Command(d, mailapp.DescribeSync(), application.APIShape{Area: area, Errors: append([]application.Code{application.Invalid}, effects...)}, "POST /mail/sync", func(ctx context.Context, in MailSyncRequest) (application.Change[MailSynced], error) {
			locks, err := db.ResourceLocks(ctx, d.Cfg.DBPath, []string{mail.PassResource}, db.ShortLockWait)
			if err != nil {
				return application.Change[MailSynced]{}, passBusy(err)
			}
			defer func() { _ = locks.Close() }()
			done, err := mailapp.New(d.DB, d.Cfg, d.Env, mail.WallClock).WithOpen(passes.open()).WithClassifiers(m.classifiers).Sync(db.WithHeld(ctx, locks), mailapp.SyncInput{Account: in.Account})
			if errors.As(err, new(*db.LockBusy)) {
				return application.Change[MailSynced]{}, passBusy(err)
			}
			if err != nil && done.AuditID == 0 {
				return application.Change[MailSynced]{}, unknownMailAccount(err)
			}
			r := done.Data
			outcome := outcomeOK
			switch {
			case len(r.Unknown) > 0:
				outcome = outcomeUnknown
			case len(r.Partial) > 0 || len(r.Committed) > 0 && (err != nil || len(r.Failed) > 0):
				outcome = outcomePartial
			case len(r.Failed) > 0:
				outcome = outcomeFailed
			case err != nil:
				return application.Change[MailSynced]{}, err
			}
			if outcome != outcomeOK {
				return application.Change[MailSynced]{}, outcomeError(outcome, "", done.EffectIDs, err)
			}
			out := application.Change[MailSynced]{Data: MailSynced{Outcome: outcome, Accounts: []MailAccountSynced{}, Committed: append([]int64{}, r.Committed...), Skipped: append([]int64{}, r.Skipped...)}, AuditID: done.AuditID, EffectIDs: done.EffectIDs}
			for _, a := range r.Synced {
				out.Data.Accounts = append(out.Data.Accounts, MailAccountSynced{Account: a.Account, New: a.Stats.New, GoneIDs: append([]int64{}, a.Stats.GoneIDs...), FlagChanges: a.Stats.FlagChanges, Mailboxes: a.Stats.Mailboxes})
			}
			return out, nil
		}),
		api.Query(d, mailapp.DescribeReplay(), application.APIShape{Area: area, Body: application.BodyJSON, ReadOnlyPost: true, Errors: []application.Code{application.Invalid}}, "POST /mail/replay", func(ctx context.Context, in MailReplayRequest) (application.Read[MailReplay], error) {
			ctx = config.With(ctx, config.From(ctx).Detached())
			r, err := service().Replay(ctx, mailapp.ReplayInput{Account: in.Account, Limit: in.Limit})
			if err != nil {
				return application.Read[MailReplay]{}, unknownMailAccount(err)
			}
			out := MailReplay{Lines: append([]replay.Line{}, r.Data.Lines...), Summary: MailReplaySummary{Messages: r.Data.Summary.Messages, Stages: r.Data.Summary.Stages, Differ: r.Data.Summary.Differ}, Reasons: []MailReplayReason{}}
			for _, reason := range r.Data.Reasons {
				out.Reasons = append(out.Reasons, MailReplayReason{Text: reason.Text, Count: reason.Count})
			}
			return application.Read[MailReplay]{Data: out}, nil
		}),
		api.Query(d, mailapp.DescribeShadowReport(), application.APIShape{Area: area, Uncached: true, Errors: []application.Code{application.Invalid}}, "GET /mail/shadow-report", func(ctx context.Context, in MailShadowReportRequest) (application.Read[MailShadowReport], error) {
			now := time.Now()
			if _, err := report.ParseSince(ctx, d.Env, in.Since, now); err != nil {
				return application.Read[MailShadowReport]{}, &application.Error{Code: application.Invalid, Message: err.Error(), Fields: []application.Field{{Name: "since", Code: "invalid", Message: err.Error()}}, Cause: err}
			}
			r, err := service().ShadowReport(ctx, mailapp.ShadowReportInput{Since: in.Since, Now: float64(now.UnixNano()) / 1e9})
			return application.Read[MailShadowReport]{Revision: r.Revision, Data: MailShadowReport{Report: r.Data}}, err
		}),
		api.Command(d, mailapp.DescribeReindex(), application.APIShape{Area: area}, "POST /mail/reindex", func(ctx context.Context, _ struct{}) (application.Change[MailReindexed], error) {
			done, err := service().Reindex(ctx, struct{}{})
			return application.Change[MailReindexed]{Data: MailReindexed{Outcome: outcomeOK, Embedded: done.Data.Embedded}, AuditID: done.AuditID}, err
		}),
	}
}
