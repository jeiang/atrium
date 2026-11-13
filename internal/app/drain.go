package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

const (
	drainWork   = 105 * time.Second
	drainSettle = 15 * time.Second
	drainTotal  = drainWork + drainSettle
)

var errWorkDeadline = errors.New("serve: work outlived the shutdown work deadline")

type shutdown struct {
	work, settle context.Context
	last         func() error
	release      func()
}

func newShutdown() *shutdown {
	start := time.Now()
	work, stopWork := context.WithDeadlineCause(context.Background(), start.Add(drainWork), errWorkDeadline)
	settle, stopSettle := context.WithDeadline(context.Background(), start.Add(drainTotal))
	return &shutdown{work: work, settle: settle, last: func() error { return nil }, release: func() {
		stopWork()
		stopSettle()
	}}
}

func (a *App) drainWork(sd *shutdown) error {
	if a.stopWork != nil {
		stop := context.AfterFunc(sd.work, func() { a.stopWork(context.Cause(sd.work)) })
		defer stop()
	}
	var wg sync.WaitGroup
	var owners, sched error
	if a.rt != nil {
		wg.Go(func() { owners = a.rt.drainOwners(sd.work, sd.settle) })
		wg.Go(func() { sched = a.rt.drainScheduler(sd.work, sd.settle) })
	}
	if a.live != nil {
		wg.Go(func() {
			if err := a.live.drain(sd.work); err != nil {
				log.Warn("assistant runs were canceled at the work deadline and stopped", logx.Err, err.Error())
			}
		})
	}
	wg.Wait()
	var owned error
	if a.rt != nil {
		owned = a.rt.settleOwned(sd.settle)
	}
	owned = errors.Join(owned, a.settleDrains(sd.settle))
	if a.stopWork != nil {
		a.stopWork(context.Canceled)
	}
	return errors.Join(owners, sched, owned, sd.last())
}

func (a *App) drainOnStop(drain func(context.Context) error) {
	a.drainMu.Lock()
	a.drains = append(a.drains, drain)
	a.drainMu.Unlock()
}

func (a *App) settleDrains(settle context.Context) error {
	a.drainMu.Lock()
	drains := a.drains
	a.drains = nil
	a.drainMu.Unlock()
	errs := make([]error, len(drains))
	var wg sync.WaitGroup
	for i, drain := range drains {
		wg.Go(func() {
			if err := drain(settle); err != nil {
				errs[i] = fmt.Errorf("could not drain work before closing the database: %w", err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

func checkpoint(ctx context.Context, d *db.DB) error {
	c, err := d.Checkpoint(ctx, time.Second)
	if err != nil {
		return fmt.Errorf("the final checkpoint failed: %w", err)
	}
	if c.Busy {
		return errors.New("the final checkpoint is incomplete: the database was busy")
	}
	return nil
}
