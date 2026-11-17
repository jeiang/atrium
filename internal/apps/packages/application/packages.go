package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/packages/tracking"
	"github.com/jeiang/atrium/internal/apps/packages/watches"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

const (
	PackagesName       application.Name = "packages.list"
	PackageAddName     application.Name = "packages.add"
	PackageDismissName application.Name = "packages.dismiss"
	TrackName          application.Name = "packages.track"
	UntrackName        application.Name = "packages.untrack"
	PollName           application.Name = "packages.poll"
)

type Package = watches.Package

type PackagesInput struct{ ActiveOnly bool }
type PackagesResult struct {
	Packages []watches.Package    `json:"packages"`
	Ship24   bool                 `json:"ship24_connected"`
	Quota    tracking.QuotaStatus `json:"quota"`
}
type PackageInput struct{ ID int64 }
type AddInput struct{ Carrier, Number, Merchant string }
type AddResult struct {
	PackageID int64 `json:"package_id"`
}
type TrackInput struct {
	ProposalID                         *int64
	PackageID                          int64
	Number, Carrier, Country, Postcode string
}
type TrackResult struct{ Tracking tracking.Result }
type UntrackResult struct {
	PackageID int64  `json:"package_id"`
	TrackerID string `json:"tracker_id"`
}
type PollResult struct{ Poll tracking.PollResult }

type TrackingFailure struct{ Err error }

func (e *TrackingFailure) Error() string { return e.Err.Error() }
func (e *TrackingFailure) Unwrap() error { return e.Err }

func DescribePackages() application.Definition {
	return application.Describe[PackagesInput, PackagesResult](PackagesName, application.Query, []application.Authority{application.Operator, application.Agent}, application.NoEffect, false)
}
func DescribeAdd() application.Definition {
	return application.Describe[AddInput, AddResult](PackageAddName, application.Command, []application.Authority{application.Operator, application.Agent}, application.NoEffect, false)
}
func DescribePackageDismiss() application.Definition {
	return application.Describe[PackageInput, struct{}](PackageDismissName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeTrack() application.Definition {
	return application.Describe[TrackInput, TrackResult](TrackName, application.Command, []application.Authority{application.Operator, application.Approved}, application.External, true)
}
func DescribeUntrack() application.Definition {
	return application.Describe[PackageInput, UntrackResult](UntrackName, application.Command, []application.Authority{application.Operator, application.Approved}, application.External, true)
}
func DescribePoll() application.Definition {
	return application.Describe[struct{}, PollResult](PollName, application.Command, []application.Authority{application.Operator, application.System}, application.External, false)
}

func (s *Service) Packages(ctx context.Context, in PackagesInput) (application.Read[PackagesResult], error) {
	var data PackagesResult
	now := s.now()
	rev, err := s.snapshot(ctx, PackagesName, in, func(q db.Querier) error {
		var err error
		if data.Packages, err = watches.ListPackages(ctx, q, s.env, now, in.ActiveOnly, false); err != nil {
			return err
		}
		if data.Quota, err = tracking.Quota(ctx, q, s.env, now); err != nil {
			return err
		}
		data.Ship24, err = tracking.Connected(ctx, q)
		return err
	})
	return application.Read[PackagesResult]{Revision: rev, Data: data}, err
}

func (s *Service) Add(ctx context.Context, in AddInput) (application.Change[AddResult], error) {
	number := strings.TrimSpace(in.Number)
	carrier := strings.TrimSpace(in.Carrier)
	merchant := strings.TrimSpace(in.Merchant)
	var id int64
	var err error
	switch {
	case number == "":
		err = invalid("tracking_number", "tracking_number is required")
	case len(number) > 64:
		err = invalid("tracking_number", "tracking_number is too long")
	case len(carrier) > 64:
		err = invalid("carrier", "carrier is too long")
	case len(merchant) > 200:
		err = invalid("merchant", "merchant is too long")
	default:
		var added bool
		id, added, err = watches.AddPackage(ctx, s.db.Write, carrier, number, merchant, s.now())
		if err == nil && !added {
			err = &application.Error{Code: application.Conflict, Message: "package " + strconv.FormatInt(id, 10) + " already has this tracking number"}
		}
	}
	auditID := s.audit(ctx, PackageAddName, number, err, nil)
	if err != nil {
		return application.Change[AddResult]{}, err
	}
	return application.Change[AddResult]{Data: AddResult{PackageID: id}, AuditID: auditID}, nil
}

func (s *Service) PackageDismiss(ctx context.Context, in PackageInput) (application.Change[struct{}], error) {
	found, err := watches.DismissPackage(ctx, s.db.Write, in.ID)
	if err == nil && !found {
		err = notFound("unknown package")
	}
	auditID := s.audit(ctx, PackageDismissName, strconv.FormatInt(in.ID, 10), err, nil)
	return application.Change[struct{}]{AuditID: auditID}, err
}

func trackingJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func (s *Service) Track(ctx context.Context, in TrackInput) (application.Change[TrackResult], error) {
	result, effectID, target, err := s.track(ctx, in)
	var effects []int64
	if effectID != 0 {
		effects = []int64{effectID}
	}
	auditID := s.audit(ctx, TrackName, target, err, effects)
	if err != nil {
		return application.Change[TrackResult]{}, err
	}
	return application.Change[TrackResult]{Data: TrackResult{Tracking: result}, EffectIDs: effects, AuditID: auditID}, nil
}

func scope(ctx context.Context) (*int64, *int64) {
	inv, ok := application.InvocationOf(ctx)
	if !ok {
		return nil, nil
	}
	return &inv.ThreadID, &inv.RunID
}

func (s *Service) track(ctx context.Context, in TrackInput) (tracking.Result, int64, string, error) {
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
	thread, run := scope(ctx)
	ticket, err := s.effects.Begin(ctx, ledger.New{
		Origin: "tracking", Operation: "track_package", OperationKey: "track_package:" + identity,
		Target: identity, ThreadID: thread, RunID: run, ProposalID: in.ProposalID,
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
		settled := ledger.Failed
		if tracking.OutcomeUnknown(err) {
			settled = ledger.Unknown
		}
		_ = s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: settled, Target: identity, Error: err.Error()}, nil)
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

func (s *Service) Untrack(ctx context.Context, in PackageInput) (application.Change[UntrackResult], error) {
	out, effectID, err := s.untrack(ctx, in)
	var effects []int64
	if effectID != 0 {
		effects = []int64{effectID}
	}
	auditID := s.audit(ctx, UntrackName, strconv.FormatInt(in.ID, 10), err, effects)
	if err != nil {
		return application.Change[UntrackResult]{}, err
	}
	return application.Change[UntrackResult]{Data: out, EffectIDs: effects, AuditID: auditID}, nil
}

func (s *Service) untrack(ctx context.Context, in PackageInput) (UntrackResult, int64, error) {
	target, err := tracking.Tracked(ctx, s.db.Read, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return UntrackResult{}, 0, notFound("unknown package")
	}
	if err != nil {
		return UntrackResult{}, 0, err
	}
	if target.TrackerID == "" {
		return UntrackResult{}, 0, &application.Error{Code: application.Conflict, Message: "package " + strconv.FormatInt(in.ID, 10) + " is not tracked with Ship24"}
	}
	identity := tracking.Identity(target.Carrier, target.TrackingNumber)
	thread, run := scope(ctx)
	ticket, err := s.effects.Begin(ctx, ledger.New{
		Origin: "tracking", Operation: "untrack_package", OperationKey: "untrack_package:" + identity + ":" + target.TrackerID,
		Target: identity, ThreadID: thread, RunID: run, Resources: []ledger.Resource{{Key: identity}},
	}, nil)
	if err != nil {
		return UntrackResult{}, 0, err
	}
	if _, err := tracking.Unsubscribe(ctx, s.db.Read, tracking.Undo{Op: tracking.UndoUntrack, PackageID: target.ID, TrackerID: target.TrackerID}); err != nil {
		settled := ledger.Failed
		if tracking.OutcomeUnknown(err) {
			settled = ledger.Unknown
		}
		_ = s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: settled, Target: identity, Error: err.Error()}, nil)
		if settled == ledger.Unknown {
			return UntrackResult{}, ticket.ID, &application.Error{Code: application.Unknown, EffectIDs: []int64{ticket.ID}, Cause: err,
				Message: "the Ship24 unsubscribe outcome is unknown: " + err.Error()}
		}
		return UntrackResult{}, ticket.ID, TrackingError(err)
	}
	if err := s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
		State: ledger.Done, Target: identity, After: trackingJSON(map[string]any{"untracked": target.TrackerID}),
	}, func(ctx context.Context, tx *sql.Tx) error {
		return tracking.Untrack(ctx, tx, target.ID)
	}); err != nil {
		return UntrackResult{}, ticket.ID, err
	}
	return UntrackResult{PackageID: target.ID, TrackerID: target.TrackerID}, ticket.ID, nil
}

func (s *Service) Poll(ctx context.Context, _ struct{}) (application.Change[PollResult], error) {
	out, err := tracking.Poll(ctx, s.db, s.env, s.now())
	if err != nil {
		return application.Change[PollResult]{}, err
	}
	return application.Change[PollResult]{Data: PollResult{Poll: out}}, nil
}

func TrackingError(err error) error {
	var done *application.Error
	var quota tracking.QuotaError
	var failure tracking.Error
	var missing config.NotConfigured
	var status *tracking.StatusError
	var limited *tracking.RateLimitError
	var transport *tracking.TransportError
	switch {
	case errors.As(err, &done):
		return err
	case errors.As(err, &quota):
		return &application.Error{Code: application.Refused, Message: quota.Error(), Cause: err}
	case errors.As(err, &failure):
		return &application.Error{Code: application.Invalid, Message: failure.Error(), Cause: err}
	case errors.As(err, &missing):
		return &application.Error{Code: application.NotConfigured, Message: missing.Error(), Cause: err}
	case errors.As(err, &status):
		return &application.Error{Code: application.Unavailable, Message: status.Error(), Cause: err}
	case errors.As(err, &limited):
		return &application.Error{Code: application.Unavailable, Message: limited.Error(), Cause: err}
	case errors.As(err, &transport):
		return &application.Error{Code: application.Unavailable, Message: transport.Error(), Cause: err}
	}
	return err
}
