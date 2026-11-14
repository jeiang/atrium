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
	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/birthdays"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/web"
)

type MailLoopsRequest struct {
	State  string     `query:"state" json:"-" api:"enum=owe_reply|waiting_on_them"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}
type MailLoopsPage struct {
	Items         []openloops.Loop `json:"items"`
	NextCursor    *string          `json:"next_cursor" api:"nullable"`
	SnoozeDefault int              `json:"snooze_default"`
}
type MailCommitmentsRequest struct {
	View   string     `query:"view" json:"-" api:"enum=open|done|dismissed"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}
type MailCommitmentsPage struct {
	Items      []openloops.Commitment `json:"items"`
	NextCursor *string                `json:"next_cursor" api:"nullable"`
}
type MailPackagesRequest struct {
	Active bool       `query:"active" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}
type MailPackagesPage struct {
	Items      []watches.Package `json:"items"`
	NextCursor *string           `json:"next_cursor" api:"nullable"`
}
type MailBirthdaysRequest struct {
	Days *int64 `query:"days" json:"-" api:"min=0"`
}
type MailBirthdays struct {
	Upcoming []birthdays.Item `json:"upcoming"`
}
type MailLoopRef struct {
	ID int64 `path:"loop_id" json:"-" api:"min=1"`
}
type MailLoopSnooze struct {
	ID   int64  `path:"loop_id" json:"-" api:"min=1"`
	Days *int64 `json:"days,omitempty"`
}
type MailLoopSnoozed struct {
	Days int64 `json:"days"`
}
type MailLoopsDismiss struct {
	State string `json:"state" api:"enum=owe_reply|waiting_on_them"`
}
type MailLoopsDismissed struct {
	Count int64 `json:"count"`
}
type MailCommitmentRef struct {
	ID int64 `path:"commitment_id" json:"-" api:"min=1"`
}
type MailPackageRef struct {
	ID int64 `path:"package_id" json:"-" api:"min=1"`
}
type MailRefreshed struct {
	Result json.RawMessage `json:"result"`
}
type MailPoll struct {
	Poll tracking.PollResult `json:"poll"`
}

type mailPasses struct {
	d *web.Deps
	m Module
}

func (p mailPasses) open() mail.OpenFunc { return p.m.Clients(p.d.Cfg) }

var errServeStopping = errors.New("mail: serve is stopping")

func (p mailPasses) job(ctx context.Context, key string, fn func(context.Context) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := context.AfterFunc(p.d.Ctx, func() { cancel(context.Cause(p.d.Ctx)) })
	defer stop()
	go func() {
		select {
		case <-p.d.Stopping:
			cancel(errServeStopping)
		case <-ctx.Done():
		}
	}()
	ran := false
	_, err := module.Invoke(ctx, p.d.DB, p.d.Cfg, key, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
		ran = true
		return scheduler.Result{}, fn(ctx)
	})
	if !ran {
		return passBusy(err)
	}
	return err
}

func (p mailPasses) locked(ctx context.Context, key string, fn func(context.Context, *run.Pass) error) error {
	return p.job(ctx, key, func(ctx context.Context) error {
		pass, err := run.Prepare(ctx, p.d.Env, p.d.DB, p.d.Cfg, p.open(), mail.WallClock, p.m.classifiers)
		if err != nil {
			return err
		}
		return fn(ctx, pass)
	})
}

func withEffects(err error, ids []int64) error {
	if err == nil || len(ids) == 0 {
		return err
	}
	classified := *application.Classify(err)
	if len(classified.EffectIDs) == 0 {
		classified.EffectIDs = ids
	}
	if classified.Cause == nil {
		classified.Cause = err
	}
	return &classified
}

const busyRetryAfter = 2 * time.Second

func passBusy(err error) error {
	return busy(err, "A mail pass is already running.")
}

func busy(err error, message string) error {
	if errors.As(err, new(*db.LockBusy)) {
		return &application.Error{Code: application.Unavailable, Reason: outcomeRefused, Message: message, Cause: err, RetryAfter: busyRetryAfter}
	}
	return err
}

func mailPage[T any](items []T, c api.Cursor, limit api.Limit, fetch func(int) ([]T, error)) ([]T, *string, error) {
	offset, err := mailOffset(c)
	if err != nil {
		return nil, nil, err
	}
	if fetch != nil {
		if items, err = fetch(offset + int(limit) + 1); err != nil {
			return nil, nil, err
		}
	}
	return mailSlice(items, c, limit)
}

func mailRefreshed(change application.Change[mailapp.RefreshResult], err error) (application.Change[MailRefreshed], error) {
	out := application.Change[MailRefreshed]{AuditID: change.AuditID, EffectIDs: change.EffectIDs}
	if err != nil {
		return out, withEffects(err, change.EffectIDs)
	}
	out.Data.Result, err = json.Marshal(change.Data.Result)
	return out, err
}

func mailWorkRoutes(d *web.Deps, m Module) []application.Binding {
	var service *mailapp.Service
	svc := func() *mailapp.Service { return service }
	passes := mailPasses{d: d, m: m}
	if d != nil {
		service = mailPageService(d)
	}
	const area = "mail"
	return []application.Binding{
		api.Query(d, mailapp.DescribeOpenLoops(), application.APIShape{Area: area}, "GET /mail/loops", func(ctx context.Context, in MailLoopsRequest) (application.Read[MailLoopsPage], error) {
			var r application.Read[mailapp.OpenLoopsResult]
			items, next, err := mailPage(nil, in.Cursor, in.Limit, func(n int) ([]openloops.Loop, error) {
				var err error
				r, err = svc().OpenLoops(ctx, mailapp.OpenLoopsInput{State: in.State, Limit: n, Snooze: true})
				return r.Data.Loops, err
			})
			return application.Read[MailLoopsPage]{Revision: r.Revision, Data: MailLoopsPage{Items: items, NextCursor: next, SnoozeDefault: r.Data.SnoozeDefault}}, err
		}),
		api.Query(d, mailapp.DescribeCommitments(), application.APIShape{Area: area}, "GET /mail/commitments", func(ctx context.Context, in MailCommitmentsRequest) (application.Read[MailCommitmentsPage], error) {
			var r application.Read[mailapp.CommitmentsResult]
			items, next, err := mailPage(nil, in.Cursor, in.Limit, func(n int) ([]openloops.Commitment, error) {
				var err error
				r, err = svc().Commitments(ctx, mailapp.CommitmentsInput{Status: in.View, Limit: n})
				return r.Data.Commitments, err
			})
			return application.Read[MailCommitmentsPage]{Revision: r.Revision, Data: MailCommitmentsPage{Items: items, NextCursor: next}}, err
		}),
		api.Query(d, mailapp.DescribePackages(), application.APIShape{Area: area}, "GET /mail/packages", func(ctx context.Context, in MailPackagesRequest) (application.Read[MailPackagesPage], error) {
			r, err := svc().Packages(ctx, mailapp.PackagesInput{ActiveOnly: in.Active})
			if err != nil {
				return application.Read[MailPackagesPage]{}, err
			}
			items, next, err := mailPage(r.Data.Packages, in.Cursor, in.Limit, nil)
			return application.Read[MailPackagesPage]{Revision: r.Revision, Data: MailPackagesPage{Items: items, NextCursor: next}}, err
		}),
		api.Query(d, mailapp.DescribeUpcomingBirthdays(), application.APIShape{Area: area, Errors: []application.Code{application.NotConfigured}}, "GET /mail/birthdays", func(ctx context.Context, in MailBirthdaysRequest) (application.Read[MailBirthdays], error) {
			loc, err := config.From(ctx).Location(d.Env)
			if err != nil {
				return application.Read[MailBirthdays]{}, err
			}
			now := time.Now().In(loc)
			r, err := svc().UpcomingBirthdays(ctx, mailapp.UpcomingBirthdaysInput{Today: birthdays.Date(now.Year(), now.Month(), now.Day()), Days: in.Days})
			return application.Read[MailBirthdays]{Revision: r.Revision, Data: MailBirthdays{Upcoming: r.Data.Upcoming}}, err
		}),
		api.Command(d, mailapp.DescribeLoopSnooze(), application.APIShape{Area: area, Errors: []application.Code{application.NotFound}}, "POST /mail/loops/{loop_id}/snooze", func(ctx context.Context, in MailLoopSnooze) (application.Change[MailLoopSnoozed], error) {
			r, err := svc().LoopSnooze(ctx, mailapp.LoopSnoozeInput{ID: in.ID, Days: in.Days})
			return application.Change[MailLoopSnoozed]{Data: MailLoopSnoozed{Days: r.Data.Days}, AuditID: r.AuditID}, err
		}),
		api.Command(d, mailapp.DescribeLoopDismiss(), application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.NotFound}}, "POST /mail/loops/{loop_id}/dismiss", func(ctx context.Context, in MailLoopRef) (application.Change[struct{}], error) {
			return svc().LoopDismiss(ctx, mailapp.LoopInput{ID: in.ID})
		}),
		api.Command(d, mailapp.DescribeLoopsDismissMatching(), application.APIShape{Area: area, Errors: []application.Code{application.Invalid}}, "POST /mail/loops/dismiss", func(ctx context.Context, in MailLoopsDismiss) (application.Change[MailLoopsDismissed], error) {
			r, err := svc().LoopsDismissMatching(ctx, mailapp.LoopsDismissMatchingInput{State: in.State})
			return application.Change[MailLoopsDismissed]{Data: MailLoopsDismissed{Count: r.Data.Count}, AuditID: r.AuditID}, err
		}),
		api.Command(d, mailapp.DescribeCommitmentDone(), application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.NotFound}}, "POST /mail/commitments/{commitment_id}/done", func(ctx context.Context, in MailCommitmentRef) (application.Change[struct{}], error) {
			return svc().CommitmentDone(ctx, mailapp.CommitmentInput{ID: in.ID})
		}),
		api.Command(d, mailapp.DescribeCommitmentDismiss(), application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.NotFound}}, "POST /mail/commitments/{commitment_id}/dismiss", func(ctx context.Context, in MailCommitmentRef) (application.Change[struct{}], error) {
			return svc().CommitmentDismiss(ctx, mailapp.CommitmentInput{ID: in.ID})
		}),
		api.Command(d, mailapp.DescribePackageDismiss(), application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.NotFound}}, "POST /mail/packages/{package_id}/dismiss", func(ctx context.Context, in MailPackageRef) (application.Change[struct{}], error) {
			return svc().PackageDismiss(ctx, mailapp.PackageInput{ID: in.ID})
		}),
		api.Command(d, mailapp.DescribeRefreshLoops(), application.APIShape{Area: area, Errors: []application.Code{application.Unavailable}}, "POST /mail/loops/refresh", func(ctx context.Context, _ struct{}) (out application.Change[MailRefreshed], err error) {
			err = passes.locked(ctx, run.SyncJobKey, func(ctx context.Context, p *run.Pass) error {
				out, err = mailRefreshed(mailCLIService(d.Env)(p).WithClassifier(p.Classifier).RefreshLoops(ctx, struct{}{}))
				return err
			})
			return out, err
		}),
		api.Command(d, mailapp.DescribeRefreshWatches(), application.APIShape{Area: area, Errors: []application.Code{application.Unavailable}}, "POST /mail/watches/refresh", func(ctx context.Context, _ struct{}) (out application.Change[MailRefreshed], err error) {
			err = passes.locked(ctx, run.SyncJobKey, func(ctx context.Context, p *run.Pass) error {
				out, err = mailRefreshed(mailCLIService(d.Env)(p).WithClassifier(p.Classifier).RefreshWatches(ctx, struct{}{}))
				return err
			})
			return out, err
		}),
		api.Command(d, mailapp.DescribePollTrackers(), application.APIShape{Area: area, Errors: []application.Code{application.Unavailable}}, "POST /mail/packages/poll", func(ctx context.Context, _ struct{}) (application.Change[MailPoll], error) {
			var r application.Change[mailapp.PollTrackersResult]
			_, err := module.Invoke(ctx, d.DB, d.Cfg, module.Mail+"/"+tracking.JobPackageTracking, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
				var err error
				r, err = svc().PollTrackers(ctx, struct{}{})
				return scheduler.Result{}, err
			})
			return application.Change[MailPoll]{Data: MailPoll{Poll: r.Data.Poll}, AuditID: r.AuditID}, busy(err, "A package poll is already running.")
		}),
	}
}
