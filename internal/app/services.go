package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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
	ctx       context.Context
	set       *module.Set
	mu        sync.Mutex
	owners    map[string]*runtime.Supervisor
	work      map[string]*ownedWork
	sched     *runtime.Supervisor
	closed    bool
	unhook    func()
	recovered atomic.Bool
	attempted chan struct{}
	attempt   sync.Once
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
	rt := &services{ctx: ctx, set: set, owners: map[string]*runtime.Supervisor{}, work: map[string]*ownedWork{}, attempted: make(chan struct{})}
	a.rt = rt
	sched, err := runtime.New([]runtime.ServiceDefinition{{Name: schedulerService, Critical: true, Stall: schedulerStall, Run: func(run context.Context, s *runtime.Services) error {
		return a.scheduler(func() { s.Heartbeat(schedulerService) }).Run(run, ctx.Done())
	}}})
	if err != nil {
		return err
	}
	started := false
	rt.unhook = set.Supervise(func(snap *module.Snapshot) {
		for _, d := range a.catalog.Declarations() {
			a.startOwner(d.Name, snap)
		}
		rt.sched = sched
		if err := sched.Start(ctx); err != nil {
			log.Error("the scheduler did not start", logx.Err, err.Error())
			return
		}
		started = true
	}, a.reconcile)
	if started {
		select {
		case <-rt.attempted:
		case <-ctx.Done():
		}
	}
	return nil
}

func (a *App) scheduler(beat func()) *scheduler.Scheduler {
	rt := a.rt
	enabled := func(name string) bool { return rt.set.Current().Enabled(name) }
	return jobworker.New(a.Assistant(), a.catalog.Jobs(), jobworker.Options{
		Enabled:   enabled,
		Active:    func() bool { return enabled(module.Assistant) },
		Beat:      beat,
		Recovered: rt.recover,
		Own:       a.own,
	})
}

func (rt *services) recover(err error) {
	if err == nil {
		rt.recovered.Store(true)
	}
	rt.attempt.Do(func() { close(rt.attempted) })
}

func (rt *services) ready() error {
	if !rt.recovered.Load() {
		return errors.New("scheduler recovery has not finished")
	}
	return rt.healthy()
}

func (rt *services) healthy() error {
	rt.mu.Lock()
	sups := make([]*runtime.Supervisor, 0, len(rt.owners)+1)
	if rt.sched != nil {
		sups = append(sups, rt.sched)
	}
	for _, sup := range rt.owners {
		sups = append(sups, sup)
	}
	rt.mu.Unlock()
	var err error
	for _, sup := range sups {
		for _, svc := range sup.Health().Services {
			if svc.Critical && (svc.Status != runtime.StatusRunning && svc.Status != runtime.StatusStarting || svc.Stalled) {
				err = errors.Join(err, fmt.Errorf("service %s is %s%s", svc.Name, svc.Status, stalled(svc)))
			}
		}
	}
	return err
}

func stalled(svc runtime.ServiceHealth) string {
	if svc.Stalled {
		return " and stalled"
	}
	return ""
}

func (a *App) ready() error {
	if a.rt == nil {
		return errors.New("background services have not started")
	}
	return a.rt.ready()
}

func (a *App) alive() bool { return a.ready() == nil }

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
		return nil, nil, scheduler.ErrInterrupted
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
	for _, w := range work {
		w.cancel(scheduler.ErrInterrupted)
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
