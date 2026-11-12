package app

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
)

const reconcileTimeout = 30 * time.Second

func (a *App) reconcile(prev, next *module.Snapshot) func() {
	var changed []string
	for _, d := range a.catalog.Declarations() {
		if prev.Generation(d.Name) != next.Generation(d.Name) {
			changed = append(changed, d.Name)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	a.rt.set.Halt(changed)
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()
	for _, owner := range changed {
		if err := a.rt.stopOwner(ctx, owner); err != nil {
			log.Error("module work did not drain before the change", logx.Module, owner, logx.Err, err.Error())
		}
	}
	return func() {
		for _, owner := range changed {
			a.startOwner(owner, next)
		}
	}
}
