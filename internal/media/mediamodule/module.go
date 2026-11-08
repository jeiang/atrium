package mediamodule

import (
	"github.com/jeiang/atrium/internal/assistant/registry"
	mediatools "github.com/jeiang/atrium/internal/assistant/tools/media"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/media/mediacmd"
	"github.com/jeiang/atrium/internal/media/pages"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Now func() float64
}

func (m Module) Register(mc module.Context) module.Registration {
	return module.Registration{
		Name: module.Media,
		Requires: []module.Requirement{
			module.Setting(media.KeyRoot),
			module.Setting(media.KeyQbitURL),
		},
		Pages: []web.Page{{
			Module:    pages.Module(pages.Options{Now: m.Now}),
			Templates: pages.Templates(),
			Public:    pages.PublicCheck,
			Nav: []web.NavItem{
				{Group: "Media", Label: "Downloads", URL: "/media/downloads", Icon: "download", Page: "media_downloads", Order: 50},
				{Group: "Media", Label: "Channels", URL: "/media/channels", Icon: "tv", Page: "media_channels", Order: 50},
				{Group: "Media", Label: "Library", URL: "/media/library", Icon: "film", Page: "media_library", Order: 50},
			},
		}},
		Commands: []*cli.Command{mediacmd.Command(mc.Env, m.Now)},
		Tools: func(r *registry.Registry) error {
			return r.Add(mediatools.Tools()...)
		},
	}
}
