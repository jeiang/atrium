package coremodule

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/connections"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
	"github.com/jeiang/atrium/internal/web"
)

const (
	AppsName      application.Name = "apps.list"
	AppSwitchName application.Name = "apps.switch"
)

type Apps struct {
	Apps []AppEntry `json:"apps"`
}

type AppEntry struct {
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	Enabled    bool                  `json:"enabled"`
	State      string                `json:"state" api:"enum=enabled|off|needs-settings|unavailable"`
	Reason     string                `json:"reason,omitempty"`
	Switchable bool                  `json:"switchable"`
	PinSource  string                `json:"pin_source,omitempty"`
	Depends    []AppLink             `json:"depends"`
	Unmet      []string              `json:"unmet"`
	Dependents []AppLink             `json:"dependents"`
	Needs      []connections.Binding `json:"needs"`
	Nav        []AppNav              `json:"nav"`
	Revision   application.Revision  `json:"revision"`
}

type AppLink struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type AppNav struct {
	Label    string `json:"label"`
	URL      string `json:"url"`
	Icon     string `json:"icon,omitempty"`
	Settings bool   `json:"settings"`
}

type AppSwitch struct {
	ID      string `path:"id" json:"-"`
	Enabled *bool  `json:"enabled"`
	IfMatch string `header:"If-Match" json:"-"`
}

func describeApps() application.Definition {
	return application.Describe[struct{}, Apps](AppsName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func describeAppSwitch() application.Definition {
	return application.Describe[AppSwitch, AppEntry](AppSwitchName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func (m Module) appsOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{describeApps(), describeAppSwitch()}, m.appsRoutes(nil)
}

func (m Module) appsRoutes(d *web.Deps) []application.Binding {
	settingsSvc := func() *settingsapp.Service { return settingsapp.Server(d.DB, d.Settings, m.Guard) }
	return []application.Binding{
		api.Query(d, describeApps(), application.APIShape{Area: "settings"},
			"GET /apps", func(ctx context.Context, _ struct{}) (application.Read[Apps], error) {
				out, err := appEntries(ctx, d, settingsSvc(), "")
				if err != nil {
					return application.Read[Apps]{}, err
				}
				return revised(Apps{Apps: out})
			}),
		api.Command(d, describeAppSwitch(), application.APIShape{Area: "settings", Errors: []application.Code{application.Invalid, application.NotFound, application.Refused, application.Precondition}},
			"PUT /apps/{id}/enabled", func(ctx context.Context, in AppSwitch) (application.Change[AppEntry], error) {
				return switchApp(ctx, d, settingsSvc(), in)
			}),
	}
}

func appError(err error) error {
	if unknown, ok := errors.AsType[*module.UnknownApp](err); ok {
		return &application.Error{Code: application.NotFound, Message: unknown.Error(), Cause: err}
	}
	if refused, ok := errors.AsType[*module.SwitchRefused](err); ok {
		return &application.Error{Code: application.Refused, Reason: refused.Reason, Message: refused.Error(), Cause: err}
	}
	if needs, ok := errors.AsType[*module.NeedsSettingsError](err); ok {
		return &application.Error{Code: application.Refused, Reason: "needs_settings", Message: needs.Error(), Cause: err}
	}
	if unavailable, ok := errors.AsType[*module.UnavailableError](err); ok {
		return &application.Error{Code: application.Refused, Reason: "unavailable", Message: unavailable.Error(), Cause: err}
	}
	return err
}

func switchApp(ctx context.Context, d *web.Deps, svc *settingsapp.Service, in AppSwitch) (application.Change[AppEntry], error) {
	if in.Enabled == nil {
		const msg = "Say whether the app is enabled."
		return application.Change[AppEntry]{}, &application.Error{Code: application.Invalid, Message: msg, Fields: []application.Field{{Name: "enabled", Code: string(application.Invalid), Message: msg}}}
	}
	set := module.SetFrom(ctx)
	if set == nil {
		return application.Change[AppEntry]{}, &application.Error{Code: application.Unavailable, Message: "app states are not evaluated yet"}
	}
	if err := set.CheckSwitch(ctx, d.DB.Read, in.ID, *in.Enabled); err != nil {
		return application.Change[AppEntry]{}, appError(err)
	}
	change, err := svc.Patch(ctx, settingsapp.PatchInput{Values: []settingsapp.Value{{Key: module.FlagKey(in.ID), Value: strconv.FormatBool(*in.Enabled)}}, IfMatch: in.IfMatch})
	if e, ok := errors.AsType[*application.Error](err); ok && e.Code == application.Precondition {
		revision, revErr := svc.Revision(ctx, nil)
		if revErr != nil {
			return application.Change[AppEntry]{}, errors.Join(err, revErr)
		}
		return application.Change[AppEntry]{}, web.WithBody(err, map[string]any{"revision": revision})
	}
	if err != nil {
		return application.Change[AppEntry]{}, appError(err)
	}
	entries, err := appEntries(ctx, d, svc, in.ID)
	if err != nil {
		return application.Change[AppEntry]{}, err
	}
	if len(entries) == 0 {
		return application.Change[AppEntry]{}, appError(&module.UnknownApp{ID: in.ID})
	}
	return application.Change[AppEntry]{Data: entries[0], AuditID: change.AuditID}, nil
}

func appEntries(ctx context.Context, d *web.Deps, svc *settingsapp.Service, only string) ([]AppEntry, error) {
	set := module.SetFrom(ctx)
	if set == nil {
		return []AppEntry{}, nil
	}
	apps, err := set.Apps(ctx, d.DB.Read)
	if err != nil {
		return nil, err
	}
	conns, err := connectionService(d)
	if err != nil {
		return nil, err
	}
	var needs []connections.Binding
	if deps, err := conns.Dependencies(ctx, connectionsapp.DependenciesInput{}); err != nil {
		log.Warn("app list without connection needs", logx.Err, err.Error())
	} else {
		needs = deps.Data.Needs
	}
	revision, err := svc.Revision(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := []AppEntry{}
	for _, app := range apps {
		if only != "" && app.ID != only {
			continue
		}
		out = append(out, appEntry(app, needs, revision))
	}
	return out, nil
}

func appEntry(app module.App, needs []connections.Binding, revision application.Revision) AppEntry {
	e := AppEntry{ID: app.ID, Name: app.Name, Enabled: app.Enabled, State: string(app.State), Reason: app.Reason, Switchable: app.PinSource == "",
		PinSource: app.PinSource, Depends: links(app.Depends), Unmet: []string{}, Dependents: links(app.Dependents),
		Needs: []connections.Binding{}, Nav: []AppNav{}, Revision: revision}
	for _, l := range app.Depends {
		if !l.Enabled {
			e.Unmet = append(e.Unmet, l.ID)
		}
	}
	for _, n := range needs {
		if n.Module == app.ID {
			e.Needs = append(e.Needs, n)
		}
	}
	for _, item := range app.Nav {
		e.Nav = append(e.Nav, AppNav{Label: item.Label, URL: item.URL, Icon: item.Icon, Settings: item.Settings})
	}
	return e
}

func links(in []module.AppLink) []AppLink {
	out := make([]AppLink, len(in))
	for i, l := range in {
		out[i] = AppLink{ID: l.ID, Name: l.Name, Enabled: l.Enabled}
	}
	return out
}

const followInterval = 2 * time.Second

func settingsCounter(ctx context.Context, store *state.Store) (int64, error) {
	counters, err := store.Read(ctx, nil, []string{state.Settings})
	if err != nil || len(counters) == 0 {
		return 0, err
	}
	return counters[0].Value, nil
}

func followFlags(ctx context.Context, d *db.DB, follow func(context.Context, db.Querier) (bool, error)) error {
	store := state.New(d)
	last, err := settingsCounter(ctx, store)
	if err != nil {
		return err
	}
	t := time.NewTicker(followInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		now, err := settingsCounter(ctx, store)
		if err != nil || now == last {
			continue
		}
		last = now
		if changed, err := follow(ctx, d.Read); err != nil && ctx.Err() == nil {
			log.Warn("module states did not follow the settings", logx.Err, err.Error())
		} else if changed {
			log.Info("module states followed a settings change")
		}
	}
}
