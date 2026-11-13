package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/birthdays"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/mail/watches"
)

const (
	PackagesName          application.Name = "mail.packages"
	UpcomingBirthdaysName application.Name = "mail.upcoming-birthdays"
	PackageDismissName    application.Name = "mail.package-dismiss"
	ProposeTrackingName   application.Name = "mail.propose-tracking"
	PollTrackersName      application.Name = "mail.poll-trackers"
)

type PackagesInput struct{ ActiveOnly bool }
type PackagesResult struct {
	Packages []watches.Package `json:"packages"`
}
type UpcomingBirthdaysInput struct {
	Today time.Time
	Days  *int64
}
type UpcomingBirthdaysResult struct {
	Upcoming []birthdays.Item `json:"upcoming"`
}
type PackageInput struct{ ID int64 }
type ProposeTrackingInput struct {
	ProposalID                         *int64
	PackageID                          int64
	Number, Carrier, Country, Postcode string
}
type ProposeTrackingResult struct{ Tracking tracking.Result }
type PollTrackersResult struct{ Poll tracking.PollResult }

type TrackingFailure struct{ Err error }

func (e *TrackingFailure) Error() string { return e.Err.Error() }
func (e *TrackingFailure) Unwrap() error { return e.Err }

func DescribePackages() application.Definition {
	return application.Describe[PackagesInput, PackagesResult](PackagesName, application.Query, []application.Authority{application.Operator, application.Agent}, application.NoEffect, false)
}
func DescribeUpcomingBirthdays() application.Definition {
	return application.Describe[UpcomingBirthdaysInput, UpcomingBirthdaysResult](UpcomingBirthdaysName, application.Query, []application.Authority{application.Agent}, application.NoEffect, false)
}
func DescribePackageDismiss() application.Definition {
	return application.Describe[PackageInput, struct{}](PackageDismissName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeProposeTracking() application.Definition {
	return application.Describe[ProposeTrackingInput, ProposeTrackingResult](ProposeTrackingName, application.Command, []application.Authority{application.Approved}, application.External, true)
}
func DescribePollTrackers() application.Definition {
	return application.Describe[struct{}, PollTrackersResult](PollTrackersName, application.Command, []application.Authority{application.Operator, application.System}, application.External, false)
}

func (s *Service) Packages(ctx context.Context, in PackagesInput) (application.Read[PackagesResult], error) {
	var data PackagesResult
	now := s.now()
	rev, err := s.snapshot(ctx, PackagesName, in, func(q db.Querier) error {
		var err error
		data.Packages, err = watches.ListPackages(ctx, q, s.env, now, in.ActiveOnly, false)
		return err
	})
	return application.Read[PackagesResult]{Revision: rev, Data: data}, err
}

func (s *Service) UpcomingBirthdays(ctx context.Context, in UpcomingBirthdaysInput) (application.Read[UpcomingBirthdaysResult], error) {
	var data UpcomingBirthdaysResult
	rev, err := s.snapshot(ctx, UpcomingBirthdaysName, in, func(q db.Querier) error {
		defaultDays, maxDays, err := birthdays.Limits(ctx, q)
		if err != nil {
			return err
		}
		days := int64(defaultDays)
		if in.Days != nil {
			days = max(0, min(*in.Days, int64(maxDays)))
		}
		found, ok, err := birthdays.UpcomingFor(ctx, q, in.Today, int(days))
		if err != nil {
			return err
		}
		if !ok {
			return &application.Error{Code: application.NotConfigured, Message: contacts.DirKey + " is not set"}
		}
		data.Upcoming = found
		return nil
	})
	return application.Read[UpcomingBirthdaysResult]{Revision: rev, Data: data}, err
}

func (s *Service) PackageDismiss(ctx context.Context, in PackageInput) (application.Change[struct{}], error) {
	auditID, err := s.localChange(ctx, PackageDismissName, strconv.FormatInt(in.ID, 10), "package", func() (bool, error) {
		return watches.DismissPackage(ctx, s.db.Write, in.ID)
	})
	return application.Change[struct{}]{AuditID: auditID}, err
}

func trackingJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func (s *Service) ProposeTracking(ctx context.Context, in ProposeTrackingInput) (application.Change[ProposeTrackingResult], error) {
	result, effectID, target, err := s.proposeTracking(ctx, in)
	var effects []int64
	if effectID != 0 {
		effects = []int64{effectID}
	}
	auditID := s.mailboxAudit(ctx, ProposeTrackingName, target, err, effects)
	if err != nil {
		return application.Change[ProposeTrackingResult]{}, err
	}
	return application.Change[ProposeTrackingResult]{Data: ProposeTrackingResult{Tracking: result}, EffectIDs: effects, AuditID: auditID}, nil
}

func (s *Service) proposeTracking(ctx context.Context, in ProposeTrackingInput) (tracking.Result, int64, string, error) {
	inv, _ := application.InvocationOf(ctx)
	target, settings, err := tracking.Resolve(ctx, s.db, s.env, s.now(), in.PackageID, in.Number, in.Carrier)
	if err != nil {
		return tracking.Result{}, 0, in.Number, &TrackingFailure{Err: err}
	}
	defer settings.Close()
	if in.Country != "" {
		settings.Country = in.Country
	}
	if in.Postcode != "" {
		settings.Postcode = in.Postcode
	}
	identity := tracking.Identity(target.Carrier, target.TrackingNumber)
	if target.TrackerID != "" {
		result, _, err := tracking.Refresh(ctx, s.db, s.now(), target, settings, s.env)
		if err != nil {
			return tracking.Result{}, 0, identity, &TrackingFailure{Err: err}
		}
		return result, 0, identity, nil
	}
	ticket, err := s.effects.Begin(ctx, ledger.New{
		Origin: "tracking", Operation: "track_package", OperationKey: "track_package:" + identity,
		Target: identity, ThreadID: &inv.ThreadID, RunID: &inv.RunID, ProposalID: in.ProposalID,
		Resources: []ledger.Resource{{Key: identity}},
	}, func(ctx context.Context, tx *sql.Tx) error {
		id, err := tracking.Admit(ctx, tx, target, s.now())
		if err != nil {
			return err
		}
		target.ID = id
		return nil
	})
	if err != nil {
		return tracking.Result{}, 0, identity, err
	}
	result, err := tracking.Register(ctx, s.db, s.now(), target, settings)
	if err != nil {
		settleCtx := context.WithoutCancel(ctx)
		settled := ledger.Failed
		if tracking.OutcomeUnknown(err) {
			settled = ledger.Unknown
		}
		_ = s.effects.Settle(settleCtx, ticket, ledger.Result{State: settled, Target: identity, Error: err.Error()}, nil)
		if settled == ledger.Unknown {
			return tracking.Result{}, ticket.ID, identity, &application.Error{Code: application.Unknown, EffectIDs: []int64{ticket.ID}, Cause: err,
				Message: "the Ship24 registration outcome is unknown: " + err.Error() +
					"; the write is blocked until the operator resolves it with `atrium effects resolve " + fmt.Sprint(ticket.ID) + " --outcome applied`"}
		}
		return tracking.Result{}, ticket.ID, identity, &TrackingFailure{Err: err}
	}
	undo := tracking.Undo{Op: tracking.UndoUntrack, PackageID: target.ID, TrackerID: result.TrackerID, Before: target}
	if err := s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
		State: ledger.Done, Target: identity,
		After: trackingJSON(map[string]any{"tracker_id": result.TrackerID, "status": result.Status}),
		Undo:  trackingJSON(undo),
	}, func(ctx context.Context, tx *sql.Tx) error {
		return tracking.SaveRegistration(ctx, tx, s.now(), target.ID, result.TrackerID, result.Tracking)
	}); err != nil {
		return tracking.Result{}, ticket.ID, identity, err
	}
	result.WriteID = ticket.ID
	return result, ticket.ID, identity, nil
}

func (s *Service) PollTrackers(ctx context.Context, _ struct{}) (application.Change[PollTrackersResult], error) {
	out, err := tracking.Poll(ctx, s.db, s.env, s.now())
	if err != nil {
		return application.Change[PollTrackersResult]{}, err
	}
	return application.Change[PollTrackersResult]{Data: PollTrackersResult{Poll: out}}, nil
}
