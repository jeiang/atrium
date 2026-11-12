package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/scheduler"
)

const (
	schedulerService = "scheduler"
	schedulerStall   = 10 * time.Minute
)

type services struct {
	ctx    context.Context
	set    *module.Set
	mu     sync.Mutex
	owners map[string]*runtime.Supervisor
	work   map[string]*ownedWork
	sched  *runtime.Supervisor
	closed bool
	unhook func()
}

type ownedWork struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	wg     sync.WaitGroup
}

func (a *App) serve(ctx context.Context, set *module.Set) error {
	if err := set.Current().Recover(ctx, a.db, a.config); err != nil {
		return err
	}
	rt := &services{ctx: ctx, set: set, owners: map[string]*runtime.Supervisor{}, work: map[string]*ownedWork{}}
	a.rt = rt
	if set.Current().Enabled(module.Assistant) {
		if err := a.scheduler(nil).Recover(ctx); err != nil {
			return err
		}
	}
	sched, err := runtime.New([]runtime.ServiceDefinition{{Name: schedulerService, Critical: true, Stall: schedulerStall, Run: func(ctx context.Context, s *runtime.Services) error {
		a.scheduler(func() { s.Heartbeat(schedulerService) }).Run(ctx)
		return ctx.Err()
	}}})
	if err != nil {
		return err
	}
	rt.unhook = set.Supervise(func(snap *module.Snapshot) {
		for _, d := range a.catalog.Declarations() {
			a.startOwner(d.Name, snap)
		}
		rt.sched = sched
		if err := sched.Start(ctx); err != nil {
			log.Error("the scheduler did not start", logx.Err, err.Error())
		}
	}, a.reconcile)
	return nil
}

func (a *App) scheduler(beat func()) *scheduler.Scheduler {
	set := a.rt.set
	enabled := func(name string) bool { return set.Current().Enabled(name) }
	return jobworker.New(a.Assistant(), a.catalog.Jobs(), jobworker.Options{
		Enabled: enabled,
		Active:  func() bool { return enabled(module.Assistant) },
		Beat:    beat,
		Own:     a.own,
	})
}

func (a *App) startOwner(owner string, snap *module.Snapshot) {
	defs := snap.ServicesOf(owner)
	if len(defs) == 0 {
		return
	}
	sup, err := runtime.New(defs)
	if err != nil {
		log.Error("module services are invalid", logx.Module, owner, logx.Err, err.Error())
		return
	}
	rt := a.rt
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed || rt.owners[owner] != nil {
		return
	}
	if err := sup.Start(rt.ctx); err != nil {
		log.Error("module services did not start", logx.Module, owner, logx.Err, err.Error())
		return
	}
	rt.owners[owner] = sup
}

func (a *App) own(ctx context.Context, owner string) (context.Context, func(), error) {
	rt := a.rt
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed {
		return nil, nil, &module.OwnerDisabled{Module: owner}
	}
	bound, err := module.BindOwner(ctx, owner)
	if err != nil {
		return nil, nil, err
	}
	w := rt.work[owner]
	if w == nil {
		base, cancel := context.WithCancelCause(context.WithoutCancel(rt.ctx))
		w = &ownedWork{ctx: base, cancel: cancel}
		rt.work[owner] = w
	}
	w.wg.Add(1)
	run, cancel := context.WithCancelCause(bound)
	stop := context.AfterFunc(w.ctx, func() { cancel(context.Cause(w.ctx)) })
	return run, func() {
		stop()
		cancel(nil)
		w.wg.Done()
	}, nil
}

func (rt *services) stopOwner(ctx context.Context, owner string) error {
	rt.mu.Lock()
	sup, w := rt.owners[owner], rt.work[owner]
	delete(rt.owners, owner)
	delete(rt.work, owner)
	rt.mu.Unlock()
	var err error
	if sup != nil {
		err = sup.Drain(ctx)
	}
	if w != nil {
		w.cancel(&module.OwnerDisabled{Module: owner})
		err = errors.Join(err, wait(ctx, &w.wg))
	}
	return err
}

func (rt *services) drainOwners() error {
	if rt.unhook != nil {
		rt.unhook()
	}
	rt.mu.Lock()
	rt.closed = true
	owners := make([]string, 0, len(rt.owners))
	for owner := range rt.owners {
		owners = append(owners, owner)
	}
	rt.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), workDrainTimeout)
	defer cancel()
	var err error
	for _, owner := range owners {
		rt.mu.Lock()
		sup := rt.owners[owner]
		delete(rt.owners, owner)
		rt.mu.Unlock()
		err = errors.Join(err, sup.Drain(ctx))
	}
	return err
}

func (rt *services) drainScheduler() error {
	ctx, cancel := context.WithTimeout(context.Background(), workDrainTimeout)
	defer cancel()
	var err error
	if rt.sched != nil {
		err = rt.sched.Drain(ctx)
	}
	rt.mu.Lock()
	work := rt.work
	rt.work = map[string]*ownedWork{}
	rt.mu.Unlock()
	for owner, w := range work {
		w.cancel(&module.OwnerDisabled{Module: owner})
		err = errors.Join(err, wait(ctx, &w.wg))
	}
	return err
}

func wait(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.New("owned jobs outlived the drain deadline")
	}
}
