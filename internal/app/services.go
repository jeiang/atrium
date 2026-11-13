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
	recoveryService  = "recovery"
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
		Beat:      beat,
		Recovered: rt.recover,
		Work:      a.work,
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
	return critical(rt.health(), func(svc runtime.ServiceHealth) bool {
		return svc.Status != runtime.StatusRunning && svc.Status != runtime.StatusStarting || svc.Stalled
	})
}

func (rt *services) health() []runtime.ServiceHealth {
	rt.mu.Lock()
	sups := make([]*runtime.Supervisor, 0, len(rt.owners)+1)
	if rt.sched != nil {
		sups = append(sups, rt.sched)
	}
	for _, sup := range rt.owners {
		sups = append(sups, sup)
	}
	rt.mu.Unlock()
	recovery := runtime.ServiceHealth{Name: recoveryService, Critical: true, Status: runtime.StatusStarting}
	if rt.recovered.Load() {
		recovery.Status = runtime.StatusRunning
	}
	out := []runtime.ServiceHealth{recovery}
	for _, sup := range sups {
		out = append(out, sup.Health().Services...)
	}
	return out
}

func critical(services []runtime.ServiceHealth, bad func(runtime.ServiceHealth) bool) error {
	var err error
	for _, svc := range services {
		if svc.Critical && bad(svc) {
			err = errors.Join(err, fmt.Errorf("service %s is %s%s", svc.Name, svc.Status, stalled(svc)))
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
	if a.draining.Load() {
		return errors.New("serve is draining")
	}
	if a.rt == nil {
		return errors.New("background services have not started")
	}
	return a.rt.ready()
}

func (a *App) alive() bool {
	if !a.draining.Load() {
		return a.ready() == nil
	}
	return a.rt == nil || critical(a.rt.health(), func(svc runtime.ServiceHealth) bool { return svc.Stalled }) == nil
}

func (a *App) health() runtime.Health {
	h := runtime.Health{Started: a.rt != nil, Draining: a.draining.Load()}
	if a.rt == nil {
		return h
	}
	h.Services = a.rt.health()
	h.Ready = a.ready() == nil
	for _, svc := range h.Services {
		if !svc.Critical && (svc.Status == runtime.StatusBackoff || svc.Stalled) {
			h.Degraded = true
		}
	}
	return h
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

func (rt *services) drainOwners(work, settle context.Context) error {
	if rt.unhook != nil {
		rt.unhook()
	}
	rt.mu.Lock()
	rt.closed = true
	sups := make([]*runtime.Supervisor, 0, len(rt.owners))
	for owner, sup := range rt.owners {
		sups = append(sups, sup)
		delete(rt.owners, owner)
	}
	rt.mu.Unlock()
	var err error
	for _, sup := range sups {
		err = errors.Join(err, drainSupervisor(sup, work, settle))
	}
	return err
}

func (rt *services) drainScheduler(work, settle context.Context) error {
	var err error
	if rt.sched != nil {
		err = drainSupervisor(rt.sched, work, settle)
	}
	rt.mu.Lock()
	owned := rt.work
	rt.work = map[string]*ownedWork{}
	rt.mu.Unlock()
	for _, w := range owned {
		w.cancel(scheduler.ErrInterrupted)
		err = errors.Join(err, wait(settle, &w.wg))
	}
	return err
}

func drainSupervisor(sup *runtime.Supervisor, work, settle context.Context) error {
	err := sup.Drain(work)
	if err == nil {
		return nil
	}
	log.Warn("services outlived the work deadline; waiting for them to settle", logx.Err, err.Error())
	return sup.Drain(settle)
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
