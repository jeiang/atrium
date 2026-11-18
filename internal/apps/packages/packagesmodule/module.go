package packagesmodule

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	pkgapp "github.com/jeiang/atrium/internal/apps/packages/application"
	"github.com/jeiang/atrium/internal/apps/packages/packagestools"
	"github.com/jeiang/atrium/internal/apps/packages/tracking"
	"github.com/jeiang/atrium/internal/apps/packages/watches"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

const JobKey = unit.Mail + "/" + tracking.JobPackageTracking

type Module struct {
	Now  func() float64
	Jobs jobworker.Dependencies
}

func (m Module) clock() func() float64 {
	if m.Now != nil {
		return m.Now
	}
	return func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
}

func toolService(tc *tool.Context) *pkgapp.Service {
	return pkgapp.New(tc.DB, tc.Env, tc.Now, tc.Ledger)
}

func (m Module) pageService(d *web.Deps) *pkgapp.Service {
	effects, err := module.SetFrom(d.Ctx).Catalog().Ledger(d.DB, d.Env, d.Cfg, m.clock())
	if err != nil {
		panic(err)
	}
	d.DrainOnStop(effects.Drain)
	return pkgapp.New(d.DB, d.Env, m.clock(), effects)
}

func operations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			pkgapp.DescribePackages(), pkgapp.DescribeAdd(), pkgapp.DescribePackageDismiss(),
			pkgapp.DescribeTrack(), pkgapp.DescribeUntrack(), pkgapp.DescribePoll(),
		}, []application.Binding{
			{Kind: application.Tool, Target: "packages", Uses: []application.Name{pkgapp.PackagesName}},
			{Kind: application.Tool, Target: "track_package", Uses: []application.Name{pkgapp.TrackName}},
			{Kind: application.Tool, Target: "untrack_package", Uses: []application.Name{pkgapp.UntrackName}},
			{Kind: application.CLI, Target: "packages poll", Uses: []application.Name{pkgapp.PollName}},
			{Kind: application.Job, Target: JobKey, Uses: []application.Name{pkgapp.PollName}},
		}
}

func SettingGroups() []settings.Group {
	return []settings.Group{{Name: "packages", Title: "Packages", Keys: []string{tracking.KeyDestinationCountry, tracking.KeyDestinationPostcode}}}
}

func (m Module) Declare(mc module.Context) module.Declaration {
	ops, bindings := operations()
	return module.Declaration{
		Name:     unit.Packages,
		Title:    "Packages",
		App:      true,
		Depends:  []string{unit.Mail},
		Needs:    []module.Need{{Kind: tracking.KindShip24, Soft: true, Capability: "api", Features: []string{"package tracking"}}},
		Packages: []string{"internal/apps/packages"},
		Tables:   []string{"package_events", "packages"},
		OptIn:    watches.HasPackages,
		Settings: tracking.SettingsDefs(),
		Groups:   SettingGroups(),
		Tools:    packagestools.Definitions(toolService),
		Commands: []*cli.Command{pollCommand(mc.Env, m.clock())},
		Jobs:     []scheduler.Definition{trackingJob(m.Jobs)},
		MessageScans: []module.MessageScan{{Name: "watches", Run: func(ctx context.Context, d *db.DB, now float64, r module.Refresh) (any, error) {
			return watches.Refresh(ctx, d, now, watches.Options{Classifier: r.Classifier, Paid: r.Paid, Clock: r.Clock, Env: r.Env})
		}}},
		Attention:     []attention.Source{attentionSource()},
		Refs:          refs(),
		EffectOrigins: []module.EffectOrigin{{Name: "tracking", Direct: true}},
		Effects: func(d module.EffectDeps) module.Effects {
			return module.Effects{Undo: undo(d.Ledger)}
		},
		Connections: tracking.Connections(),
		Operations:  ops,
		Bindings:    append(bindings, routes(nil, nil)...),
		Pages: []web.Page{{
			Module: func(_ chi.Router, d *web.Deps) { routes(d, m.pageService(d)) },
			Nav:    []web.NavItem{{Section: "review", Label: "Packages", URL: "/apps/packages", Icon: "package", Page: "watches", Order: 30}},
		}},
	}
}

func trackingJob(deps jobworker.Dependencies) scheduler.Definition {
	return scheduler.Definition{
		Module: unit.Packages, KeyOwner: unit.Mail, Key: JobKey, Name: tracking.JobPackageTracking, Kind: scheduler.JobBuiltin,
		Prompt:   "Hourly: refresh active Ship24 package trackers (no model run).",
		Schedule: "0 * * * *", CarriedSetting: tracking.KeySchedulePackageTracking, Persistent: true, Timeout: 15 * time.Minute,
		Spend: scheduler.ModelFree, Needs: []string{unit.Packages},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			done, err := pkgapp.New(e.Service.DB, e.Service.Env, e.Now, nil).Poll(ctx, struct{}{})
			if err == nil && done.Data.Poll.Unmet != nil {
				return jobworker.Result{}, done.Data.Poll.Unmet
			}
			return jobworker.Result{}, err
		}),
	}
}

func refs() []module.RefKind {
	return []module.RefKind{
		module.NumberedRef("package", func(ctx context.Context, q db.Querier, id int64, number string) (*module.Ref, error) {
			label, err := tracking.Label(ctx, q, id)
			return &module.Ref{Label: label, Line: "About tracked package " + number + "."}, err
		}),
	}
}

func undo(store *ledger.Store) []ledger.UndoDefinition {
	return []ledger.UndoDefinition{{Operation: tracking.UndoUntrack, Undoer: ledger.Undoer{
		Validate: func(raw json.RawMessage) error {
			var head struct {
				Op string `json:"op"`
			}
			if err := json.Unmarshal(raw, &head); err != nil {
				return ledger.Refused("mail undo payload is invalid")
			}
			if head.Op != tracking.UndoUntrack {
				return ledger.Refused("mail undo payload names " + head.Op)
			}
			return nil
		},
		External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
			value, err := undoPackageUntrack(ctx, store, raw)
			if err != nil {
				var missing config.NotConfigured
				if errors.As(err, &missing) {
					return ledger.Result{}, ledger.Refused(missing.Error())
				}
				return ledger.Result{}, err
			}
			after, merr := json.Marshal(value)
			if merr != nil {
				after = json.RawMessage(`{}`)
			}
			return ledger.Result{State: ledger.Done, After: after}, nil
		},
		Finalize: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage, result ledger.Result) error {
			if result.State != ledger.Done {
				return nil
			}
			var u tracking.Undo
			if err := json.Unmarshal(raw, &u); err != nil {
				return ledger.Refused("package undo payload is invalid")
			}
			return tracking.Restore(ctx, tx, u)
		}}}}
}

func undoPackageUntrack(ctx context.Context, store *ledger.Store, raw json.RawMessage) (any, error) {
	var u tracking.Undo
	if err := json.Unmarshal(raw, &u); err != nil || u.Op != tracking.UndoUntrack || u.PackageID == 0 {
		return nil, ledger.Refused("package undo payload is invalid")
	}
	result, err := tracking.Unsubscribe(ctx, store.DB.Write, u)
	if err != nil {
		var missing config.NotConfigured
		if errors.As(err, &missing) {
			return nil, ledger.Refused(missing.Error())
		}
		var failure tracking.Error
		if errors.As(err, &failure) {
			return nil, ledger.Refused(failure.Error())
		}
		return nil, err
	}
	return result, nil
}
