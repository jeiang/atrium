package torrentsmodule

import (
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/torrents"
	torrentsapp "github.com/jeiang/atrium/internal/apps/torrents/application"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/apps/torrents/pages"
	"github.com/jeiang/atrium/internal/apps/torrents/torrenttools"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	App       *torrents.App
	Now       func() float64
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
			{Kind: application.HTML, Target: "GET /apps/media/torrents", Uses: []application.Name{torrentsapp.DownloadsName}},
			{Kind: application.Tool, Target: torrenttools.NameDownloads, Uses: []application.Name{torrentsapp.DownloadsName}},
			{Kind: application.HTML, Target: "GET /apps/media/torrents/{hash}/files", Uses: []application.Name{torrentsapp.TorrentFilesName}},
			{Kind: application.HTML, Target: "POST /apps/media/torrents/{hash}/files", Uses: []application.Name{torrentsapp.SelectFilesName, torrentsapp.TorrentFilesName}},
			{Kind: application.HTML, Target: "POST /apps/media/add", Uses: []application.Name{torrentsapp.AddName}},
			{Kind: application.Tool, Target: torrenttools.NameAdd, Uses: []application.Name{torrentsapp.AddName}},
			{Kind: application.Tool, Target: torrenttools.NameControl, Uses: []application.Name{torrentsapp.ControlName}},
			{Kind: application.ProtocolOnly, Target: "GET /apps/media/add", Reason: "transport"},
		}
}

func (m Module) Declare(mc module.Context) module.Declaration {
	ops, bindings := operations()
	return module.Declaration{
		Name:  unit.Torrents,
		Title: "Torrents",
		App:   true,
		Needs: []module.Need{
			{Kind: contract.Kind, Capability: "api", Features: []string{"downloads"}},
		},
		Packages:    []string{"internal/apps/torrents"},
		Requires:    Requirements(),
		Tools:       torrenttools.Definitions(m.App),
		Operations:  ops,
		Bindings:    append(bindings, routes(nil, nil)...),
		Connections: torrents.Connections(),
		Effects: func(d module.EffectDeps) module.Effects {
			return module.Effects{Undo: m.App.Undo(d.Ledger, d.Env)}
		},
		Pages: []web.Page{{
			Module: pages.Module(pages.Options{
				Service:   m.pageService,
				Routes:    func(d *web.Deps, s *torrentsapp.Service) { routes(d, s) },
				Consumers: m.App.Consumers(),
			}),
			Templates: pages.Templates(),
		}},
	}
}
