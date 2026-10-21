package moneymodule

import (
	"github.com/jeiang/atrium/internal/assistant/registry"
	moneytools "github.com/jeiang/atrium/internal/assistant/tools/money"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/money"
	moneypages "github.com/jeiang/atrium/internal/money/pages"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Now func() float64
}

func (m Module) Register(_ module.Context) module.Registration {
	return module.Registration{
		Name: module.Money,
		Soft: []string{module.Mail},
		Pages: []web.Page{{
			Module:    moneypages.Module(moneypages.Options{Now: m.Now}),
			Templates: moneypages.Templates(),
		}},
		Commands: []*cli.Command{money.Command(m.Now)},
		Leaves: map[string]module.Leaf{
			"money refresh": {Needs: []string{module.Mail}},
		},
		Tools: func(r *registry.Registry) error {
			return r.Add(moneytools.Tools()...)
		},
	}
}
