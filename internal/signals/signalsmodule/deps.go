package signalsmodule

import (
	commitmentsapp "github.com/jeiang/atrium/internal/commitments/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

func newService(d *web.Deps) *Service {
	effects, err := module.SetFrom(d.Ctx).Catalog().Ledger(d.DB, d.Env, d.Cfg, WallClock)
	if err != nil {
		panic(err)
	}
	d.DrainOnStop(effects.Drain)
	return New(d.DB, WallClock, commitmentsapp.New(d.DB, effects, WallClock))
}
