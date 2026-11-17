package torrentsmodule

import (
	"context"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/torrents"
	torrentsapp "github.com/jeiang/atrium/internal/apps/torrents/application"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/apps/torrents/torrenttools"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/review"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	App       *torrents.App
	Now       func() float64
	Resources module.Resources
}

func Requirements() []module.Requirement {
	return []module.Requirement{module.Connection(string(contract.Kind))}
}

func (m Module) clock() func() float64 {
	if m.Now != nil {
		return m.Now
	}
	return func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
}

func (m Module) pageService(d *web.Deps) *torrentsapp.Service {
	effects, err := module.SetFrom(d.Ctx).Catalog().Ledger(d.DB, d.Env, d.Cfg, m.clock())
	if err != nil {
		panic(err)
	}
	d.DrainOnStop(effects.Drain)
	return torrentsapp.New(torrentsapp.Deps{DB: d.DB, Env: d.Env, Ledger: effects, App: m.App, Actor: contract.Operator})
}

func operations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			torrentsapp.DescribeDownloads(),
			torrentsapp.DescribeTorrentFiles(),
			torrentsapp.DescribeAdd(),
			torrentsapp.DescribeSelectFiles(),
			torrentsapp.DescribeControl(),
		}, []application.Binding{
			{Kind: application.Tool, Target: torrenttools.NameDownloads, Uses: []application.Name{torrentsapp.DownloadsName}},
			{Kind: application.Tool, Target: torrenttools.NameAdd, Uses: []application.Name{torrentsapp.AddName}},
			{Kind: application.Tool, Target: torrenttools.NameControl, Uses: []application.Name{torrentsapp.ControlName}},
			{Kind: application.ProtocolOnly, Target: "downloads-watch", Reason: "lifecycle"},
		}
}

func (m Module) Declare(mc module.Context) module.Declaration {
	ops, bindings := operations()
	return module.Declaration{
		Name:     unit.Torrents,
		Title:    "Torrents",
		App:      true,
		Inherits: unit.Media,
		Needs: []module.Need{
			{Kind: contract.Kind, Capability: "api", Features: []string{"downloads"}},
		},
		Packages:    []string{"internal/apps/torrents"},
		Requires:    Requirements(),
		Tools:       torrenttools.Definitions(m.App),
		Operations:  ops,
		Bindings:    append(bindings, routes(nil, nil)...),
		Connections: torrents.Connections(),
		EffectCategories: []review.Category{{Name: "downloads", Operations: []string{
			torrents.OpAddMagnet, torrents.OpAddFile, torrents.OpPriority, torrents.OpTagAdd, torrents.OpTagRemove,
			torrents.OpStart, torrents.OpStop, torrents.OpDelete, torrents.UndoTorrent, torrents.UndoTorrentState, torrents.UndoTorrentTag,
		}}},
		Effects: func(d module.EffectDeps) module.Effects {
			return module.Effects{Undo: m.App.Undo(d.Ledger, d.Env)}
		},
		Services: []runtime.ServiceDefinition{{Name: "downloads-watch", Run: func(ctx context.Context, _ *runtime.Services) error {
			d, _, err := m.Resources(ctx)
			if err != nil {
				return err
			}
			return m.App.Watch(ctx, contract.Deps{DB: d, Env: mc.Env})
		}}},
		Pages: []web.Page{{
			Module: func(_ chi.Router, d *web.Deps) { routes(d, m.pageService(d)) },
		}},
	}
}
