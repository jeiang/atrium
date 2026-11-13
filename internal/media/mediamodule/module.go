package mediamodule

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	mediatools "github.com/jeiang/atrium/internal/assistant/tools/media"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/media/mediacmd"
	"github.com/jeiang/atrium/internal/media/pages"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Now       func() float64
	Jobs      jobworker.Dependencies
	Resources module.Resources
}

func Requirements() []module.Requirement {
	return []module.Requirement{
		module.Connection(string(media.KindMediaAgent)),
		module.Connection(string(media.KindQbittorrent)),
	}
}

func SettingGroups() []settings.Group {
	return []settings.Group{{Name: "library", Title: "Library", Keys: []string{media.KeyLinkTTL}}}
}

func ToolDefinitions() []module.ToolDefinition {
	return mediatools.Definitions()
}

func (m Module) Declare(mc module.Context) module.Declaration {
	var operations []application.Definition
	var bindings []application.Binding
	ops, binds := downloadOperations()
	operations, bindings = append(operations, ops...), append(bindings, binds...)
	ops, binds = channelOperations()
	operations, bindings = append(operations, ops...), append(bindings, binds...)
	ops, binds = libraryOperations()
	operations, bindings = append(operations, ops...), append(bindings, binds...)
	ops, binds = filingOperations()
	operations, bindings = append(operations, ops...), append(bindings, binds...)
	return module.Declaration{
		Operations: operations,
		Bindings:   bindings,
		Name:       module.Media,
		Requires:   Requirements(),
		Jobs:       mediacmd.Jobs(m.Jobs, openService),
		Services: []runtime.ServiceDefinition{{Name: "media-catalogue", Run: func(ctx context.Context, _ *runtime.Services) error {
			d, _, err := m.Resources(ctx)
			if err != nil {
				return err
			}
			return mediaapp.New(mediaapp.Deps{DB: d, Env: mc.Env, Now: clock(m.Now)}).RefreshCatalogue(ctx)
		}}},
		Settings: media.SettingsDefs(),
		Host:     media.HostSettings(),
		Pages: []web.Page{{
			Module:    pages.Module(pages.Options{Service: pageService(m.Now)}),
			Templates: pages.Templates(),
			Public:    publicCheck,
			Nav: []web.NavItem{
				{Section: "apps", Label: "Downloads", URL: "/apps/media/downloads", Icon: "download", Page: "media_downloads", Order: 10},
				{Section: "apps", Label: "Channels", URL: "/apps/media/channels", Icon: "tv", Page: "media_channels", Order: 20},
				{Section: "apps", Label: "Library", URL: "/apps/media/library", Icon: "film", Page: "media_library", Order: 30},
			},
		}},
		Groups:   SettingGroups(),
		Commands: []*cli.Command{mediacmd.Command(mc.Env, m.Now, openService)},
		Tools:    ToolDefinitions(),
		Effects: func(d module.EffectDeps) module.Effects {
			return module.Effects{
				Undo:    mediatools.Undo(d.Ledger, d.Env),
				Resolve: media.Resolve(d.Ledger),
			}
		},
		Connections: media.Connections(),
	}
}
