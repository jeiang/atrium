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

func (m Module) Register(_ module.Context) module.Registration {
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
		}},
		Commands: []*cli.Command{mediacmd.Command(m.Now)},
		Tools: func(r *registry.Registry) error {
			return r.Add(mediatools.Tools()...)
		},
	}
}
