package packagesmodule

import (
	"context"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	pkgapp "github.com/jeiang/atrium/internal/apps/packages/application"
	"github.com/jeiang/atrium/internal/apps/packages/tracking"
	"github.com/jeiang/atrium/internal/apps/packages/watches"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/web"
)

type PackagesRequest struct {
	Active bool       `query:"active" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}
type PackagesPage struct {
	Items      []watches.Package    `json:"items"`
	NextCursor *string              `json:"next_cursor" api:"nullable"`
	Ship24     bool                 `json:"ship24_connected"`
	Quota      tracking.QuotaStatus `json:"quota"`
}
type PackageAdd struct {
	TrackingNumber string `json:"tracking_number"`
	Carrier        string `json:"carrier,omitempty"`
	Merchant       string `json:"merchant,omitempty"`
}
type PackageAdded struct {
	PackageID int64 `json:"package_id"`
}
type PackageRef struct {
	ID int64 `path:"package_id" json:"-" api:"min=1"`
}
type PackageTrack struct {
	ID       int64  `path:"package_id" json:"-" api:"min=1"`
	Country  string `json:"destination_country,omitempty"`
	Postcode string `json:"destination_postcode,omitempty"`
}
type PackageTracked struct {
	Tracking tracking.Result `json:"tracking"`
}
type PackageUntracked struct {
	PackageID int64  `json:"package_id"`
	TrackerID string `json:"tracker_id"`
}
type PackagesPoll struct {
	Poll tracking.PollResult `json:"poll"`
}

const (
	area           = "mail"
	busyRetryAfter = 2 * time.Second
)

func busy(err error, message string) error {
	if errors.As(err, new(*db.LockBusy)) {
		return &application.Error{Code: application.Unavailable, Reason: "refused", Message: message, Cause: err, RetryAfter: busyRetryAfter}
	}
	return err
}

func cursorError() error {
	return &application.Error{Code: application.Invalid, Message: "Invalid cursor position."}
}

func trackFailure(err error) error {
	if failure, ok := errors.AsType[*pkgapp.TrackingFailure](err); ok {
		return pkgapp.TrackingError(failure.Err)
	}
	return pkgapp.TrackingError(err)
}

func routes(d *web.Deps, s *pkgapp.Service) []application.Binding {
	return []application.Binding{
		api.Query(d, pkgapp.DescribePackages(), application.APIShape{Area: area}, "GET /packages", func(ctx context.Context, in PackagesRequest) (application.Read[PackagesPage], error) {
			r, err := s.Packages(ctx, pkgapp.PackagesInput{ActiveOnly: in.Active})
			if err != nil {
				return application.Read[PackagesPage]{}, err
			}
			offset := 0
			if in.Cursor.Set() {
				if err := in.Cursor.Last(&offset); err != nil || offset < 0 {
					return application.Read[PackagesPage]{}, cursorError()
				}
			}
			items := r.Data.Packages
			page := PackagesPage{Items: []watches.Package{}, Ship24: r.Data.Ship24, Quota: r.Data.Quota}
			if offset < len(items) {
				end := min(len(items), offset+int(in.Limit))
				page.Items = items[offset:end]
				if end < len(items) {
					page.NextCursor = in.Cursor.Next(end)
				}
			}
			return application.Read[PackagesPage]{Revision: r.Revision, Data: page}, nil
		}),
		api.Command(d, pkgapp.DescribeAdd(), application.APIShape{Area: area, Errors: []application.Code{application.Conflict}}, "POST /packages", func(ctx context.Context, in PackageAdd) (application.Change[PackageAdded], error) {
			r, err := s.Add(ctx, pkgapp.AddInput{Carrier: in.Carrier, Number: in.TrackingNumber, Merchant: in.Merchant})
			return application.Change[PackageAdded]{Data: PackageAdded{PackageID: r.Data.PackageID}, AuditID: r.AuditID}, err
		}),
		api.Command(d, pkgapp.DescribeTrack(), application.APIShape{Area: area, Errors: []application.Code{application.NotFound, application.Refused, application.NotConfigured, application.Unavailable, application.Unknown}}, "POST /packages/{package_id}/track", func(ctx context.Context, in PackageTrack) (application.Change[PackageTracked], error) {
			r, err := s.Track(ctx, pkgapp.TrackInput{PackageID: in.ID, Country: in.Country, Postcode: in.Postcode})
			if err != nil {
				return application.Change[PackageTracked]{}, trackFailure(err)
			}
			return application.Change[PackageTracked]{Data: PackageTracked{Tracking: r.Data.Tracking}, AuditID: r.AuditID, EffectIDs: r.EffectIDs}, nil
		}),
		api.Command(d, pkgapp.DescribeUntrack(), application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.NotFound, application.Conflict, application.NotConfigured, application.Unavailable, application.Unknown}}, "POST /packages/{package_id}/untrack", func(ctx context.Context, in PackageRef) (application.Change[PackageUntracked], error) {
			r, err := s.Untrack(ctx, pkgapp.PackageInput{ID: in.ID})
			if err != nil {
				return application.Change[PackageUntracked]{}, trackFailure(err)
			}
			return application.Change[PackageUntracked]{Data: PackageUntracked{PackageID: r.Data.PackageID, TrackerID: r.Data.TrackerID}, AuditID: r.AuditID, EffectIDs: r.EffectIDs}, nil
		}),
		api.Command(d, pkgapp.DescribePackageDismiss(), application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.NotFound}}, "POST /packages/{package_id}/dismiss", func(ctx context.Context, in PackageRef) (application.Change[struct{}], error) {
			return s.PackageDismiss(ctx, pkgapp.PackageInput{ID: in.ID})
		}),
		api.Command(d, pkgapp.DescribePoll(), application.APIShape{Area: area, Errors: []application.Code{application.Unavailable, application.Refused}}, "POST /packages/poll", func(ctx context.Context, _ struct{}) (application.Change[PackagesPoll], error) {
			var r application.Change[pkgapp.PollResult]
			_, err := module.Invoke(ctx, d.DB, d.Cfg, JobKey, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
				var err error
				r, err = s.Poll(ctx, struct{}{})
				return scheduler.Result{}, err
			})
			return application.Change[PackagesPoll]{Data: PackagesPoll{Poll: r.Data.Poll}, AuditID: r.AuditID}, busy(err, "A package poll is already running.")
		}),
	}
}
