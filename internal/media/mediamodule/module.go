package mediamodule

import (
	mediatools "github.com/jeiang/atrium/internal/assistant/tools/media"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/media/mediacmd"
	"github.com/jeiang/atrium/internal/media/pages"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Now func() float64
}

func Requirements() []module.Requirement {
	return []module.Requirement{
		module.HostSetting(media.KeyRoot),
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
	return module.Declaration{
		Name:     module.Media,
		Requires: Requirements(),
		Pages: []web.Page{{
			Module:    pages.Module(pages.Options{Now: m.Now}),
			Templates: pages.Templates(),
			Public:    pages.PublicCheck,
			Nav: []web.NavItem{
				{Section: "apps", Label: "Downloads", URL: "/apps/media/downloads", Icon: "download", Page: "media_downloads", Order: 10},
				{Section: "apps", Label: "Channels", URL: "/apps/media/channels", Icon: "tv", Page: "media_channels", Order: 20},
				{Section: "apps", Label: "Library", URL: "/apps/media/library", Icon: "film", Page: "media_library", Order: 30},
			},
		}},
		Groups:   SettingGroups(),
		Commands: []*cli.Command{mediacmd.Command(mc.Env, m.Now)},
		Tools:    ToolDefinitions(),
	}
}
