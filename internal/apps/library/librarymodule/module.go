package librarymodule

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/library"
	mediaapp "github.com/jeiang/atrium/internal/apps/library/application"
	"github.com/jeiang/atrium/internal/apps/library/librarytools"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/review"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/scheduler/jobcmd"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Now       func() float64
	Jobs      jobworker.Dependencies
	Resources module.Resources
	Torrents  contract.Provider
}

func Requirements() []module.Requirement {
	return []module.Requirement{
		module.Connection(string(library.KindMediaAgent), "bearer"),
	}
}

func SettingGroups() []settings.Group {
	return []settings.Group{{Name: "library", Title: "Library", Keys: []string{library.KeyLinkTTL}}}
}

func (m Module) ToolDefinitions() []module.ToolDefinition {
	return librarytools.Definitions(m.Torrents)
}

func (m Module) Declare(mc module.Context) module.Declaration {
	var operations []application.Definition
	var bindings []application.Binding
	ops, binds := queueOperations()
	operations, bindings = append(operations, ops...), append(bindings, binds...)
	ops, binds = channelOperations()
	operations, bindings = append(operations, ops...), append(bindings, binds...)
	ops, binds = libraryOperations()
	operations, bindings = append(operations, ops...), append(bindings, binds...)
	ops, binds = filingOperations()
	operations, bindings = append(operations, ops...), append(bindings, binds...)
	bindings = append(bindings, mediaRoutes(nil, nil)...)
	return module.Declaration{
		Operations: operations,
		Bindings:   append(bindings, finalBindings()...),
		Name:       unit.Media,
		Title:      "Library",
		App:        true,
		Needs: []module.Need{
			{Kind: library.KindMediaAgent, Capability: "scan", Features: []string{"media library"}},
			{Kind: library.KindMediaAgent, Soft: true, Capability: "file", Features: []string{"media filing"}},
			{Kind: library.KindMediaAgent, Soft: true, Capability: "stream", Features: []string{"library streaming"}},
			{Kind: library.KindTMDB, Soft: true, Capability: "api", Features: []string{"title lookup"}},
			{Kind: library.KindTMDB, Soft: true, Capability: "images", Features: []string{"posters"}},
			{Kind: library.KindEZTV, Soft: true, Capability: "indexer", Features: []string{"media channels"}},
		},
		Packages:         []string{"internal/apps/library"},
		Tables:           []string{"library_requests", "media_channels", "media_seen", "media_state"},
		EffectOrigins:    []module.EffectOrigin{{Name: "media", Direct: true}},
		Channels:         []string{library.NotifyChannel},
		EffectCategories: []review.Category{{Name: "library", Operations: []string{"media"}}},
		Requires:         Requirements(),
		Jobs:             mediaJobs(m.Jobs, m.Torrents),
		Services: []runtime.ServiceDefinition{{Name: "media-catalogue", Run: func(ctx context.Context, _ *runtime.Services) error {
			d, _, err := m.Resources(ctx)
			if err != nil {
				return err
			}
			return mediaapp.New(mediaapp.Deps{DB: d, Env: mc.Env, Now: clock(m.Now), Torrents: m.Torrents}).RefreshCatalogue(runtime.Unattended(ctx))
		}}},
		Settings: library.SettingsDefs(),
		Host:     library.HostSettings(),
		Pages: []web.Page{{
			Module: func(r chi.Router, d *web.Deps) {
				s := pageService(m.Now, m.Torrents)(d)
				mediaRoutes(d, s)
				fileRoutes(r, d, s)
			},
			Public: publicCheck,
			Nav: []web.NavItem{
				{Section: "apps", Label: "Downloads", URL: "/apps/media/downloads", Icon: "download", Page: "media_downloads", Order: 10},
				{Section: "apps", Label: "Channels", URL: "/apps/media/channels", Icon: "tv", Page: "media_channels", Order: 20},
				{Section: "apps", Label: "Library", URL: "/apps/media/library", Icon: "film", Page: "media_library", Order: 30},
			},
		}},
		Groups:   SettingGroups(),
		Commands: []*cli.Command{jobcmd.DelegatingSubs(mediaCommand(mc.Env, m.Now, m.Torrents), map[string]string{"poll": unit.Media + "/" + library.JobChannels, "sweep": unit.Media + "/" + library.JobSweep})},
		Tools:    m.ToolDefinitions(),
		Effects: func(d module.EffectDeps) module.Effects {
			return module.Effects{
				Undo:    mediaapp.Undo(d.Ledger, d.Env),
				Resolve: library.Resolve(d.Ledger),
			}
		},
		Connections: library.Connections(),
		Depends:     []string{unit.Torrents},
	}
}
