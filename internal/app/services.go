package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"

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
	sets      map[string]map[string]*setService
	setMu     sync.Mutex
	kick      chan struct{}
	quit      chan struct{}
	refreshed chan struct{}
	unwatch   func()
	work      map[string]*ownedWork
	sched     *runtime.Supervisor
	closed    bool
	unhook    func()
	recovered atomic.Bool
	attempted chan struct{}
	attempt   sync.Once
}

type setService struct {
	sup      *runtime.Supervisor
	revision string
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
	rt := &services{ctx: ctx, set: set, owners: map[string]*runtime.Supervisor{}, sets: map[string]map[string]*setService{}, work: map[string]*ownedWork{},
		kick: make(chan struct{}, 1), quit: make(chan struct{}), refreshed: make(chan struct{}), attempted: make(chan struct{})}
	a.rt = rt
	go a.refreshLoop()
	rt.unwatch = a.state.OnAccounts(func() {
		select {
		case rt.kick <- struct{}{}:
		default:
		}
	})
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
		Start:     asstapp.ScheduledStart,
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
	for _, owned := range rt.sets {
		for _, svc := range owned {
			sups = append(sups, svc.sup)
		}
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
	a.startServices(owner, snap)
	a.refreshSets(owner)
}

func (a *App) startServices(owner string, snap *module.Snapshot) {
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

func (a *App) refreshLoop() {
	rt := a.rt
	defer close(rt.refreshed)
	for {
		select {
		case <-rt.quit:
			return
		case <-rt.kick:
			for _, d := range a.catalog.Declarations() {
				if d.ServiceSets != nil {
					a.refreshSets(d.Name)
				}
			}
		}
	}
}

func (a *App) refreshSets(owner string) {
	rt := a.rt
	rt.setMu.Lock()
	defer rt.setMu.Unlock()
	if rt.set.Halting(owner) {
		return
	}
	var want []module.SetService
	if fn := rt.set.Current().ServiceSetsOf(owner); fn != nil {
		var err error
		if want, err = fn(rt.ctx); err != nil {
			log.Error("module service set is unavailable", logx.Module, owner, logx.Err, err.Error())
			return
		}
	}
	revisions := make(map[string]string, len(want))
	for _, w := range want {
		revisions[w.Service.Name] = w.Revision
	}
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return
	}
	current := rt.sets[owner]
	keep := make(map[string]*setService, len(want))
	var stale []*setService
	for name, svc := range current {
		if rev, ok := revisions[name]; ok && rev == svc.revision {
			keep[name] = svc
		} else {
			stale = append(stale, svc)
		}
	}
	rt.sets[owner] = keep
	rt.mu.Unlock()
	if err := drainSets(stale, reconcileTimeout); err != nil {
		log.Error("module service did not stop before the change", logx.Module, owner, logx.Err, err.Error())
	}
	for _, w := range want {
		if keep[w.Service.Name] != nil {
			continue
		}
		sup, err := runtime.New([]runtime.ServiceDefinition{w.Service})
		if err != nil {
			log.Error("module service is invalid", logx.Module, owner, logx.Err, err.Error())
			continue
		}
		sup.SetWork(a.setWork(owner))
		rt.mu.Lock()
		if rt.closed {
			rt.mu.Unlock()
			return
		}
		if err := sup.Start(rt.ctx); err != nil {
			rt.mu.Unlock()
			log.Error("module service did not start", logx.Module, owner, logx.Err, err.Error())
			continue
		}
		keep[w.Service.Name] = &setService{sup: sup, revision: w.Revision}
		rt.mu.Unlock()
	}
}

func drainSets(svcs []*setService, limit time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	errs := make([]error, len(svcs))
	var wg sync.WaitGroup
	for i, svc := range svcs {
		wg.Go(func() { errs[i] = svc.sup.Drain(ctx) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (a *App) setWork(owner string) runtime.WorkFunc {
	return func(ctx context.Context) (context.Context, func(), error) {
		run, release, err := a.own(ctx, owner)
		if err != nil {
			return nil, nil, err
		}
		if a.work == nil {
			return run, release, nil
		}
		run, cancel := context.WithCancelCause(run)
		stop := context.AfterFunc(a.work, func() { cancel(context.Cause(a.work)) })
		return run, func() {
			stop()
			cancel(nil)
			release()
		}, nil
	}
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
	rt.setMu.Lock()
	defer rt.setMu.Unlock()
	rt.mu.Lock()
	sup, w, owned := rt.owners[owner], rt.work[owner], rt.sets[owner]
	delete(rt.owners, owner)
	delete(rt.work, owner)
	delete(rt.sets, owner)
	rt.mu.Unlock()
	var err error
	if sup != nil {
		err = sup.Drain(ctx)
	}
	if w != nil {
		w.cancel(&module.OwnerDisabled{Module: owner})
	}
	for _, svc := range owned {
		err = errors.Join(err, svc.sup.Drain(ctx))
	}
	if w != nil {
		err = errors.Join(err, wait(ctx, &w.wg))
	}
	return err
}

func (rt *services) drainOwners(work, settle context.Context) error {
	if rt.unhook != nil {
		rt.unhook()
	}
	rt.unwatch()
	close(rt.quit)
	<-rt.refreshed
	rt.setMu.Lock()
	rt.mu.Lock()
	rt.closed = true
	sups := make([]*runtime.Supervisor, 0, len(rt.owners))
	for owner, sup := range rt.owners {
		sups = append(sups, sup)
		delete(rt.owners, owner)
	}
	for owner, owned := range rt.sets {
		for _, svc := range owned {
			sups = append(sups, svc.sup)
		}
		delete(rt.sets, owner)
	}
	rt.mu.Unlock()
	rt.setMu.Unlock()
	errs := make([]error, len(sups))
	var wg sync.WaitGroup
	for i, sup := range sups {
		wg.Go(func() { errs[i] = drainSupervisor(sup, work, settle) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (rt *services) drainScheduler(work, settle context.Context) error {
	if rt.sched == nil {
		return nil
	}
	return drainSupervisor(rt.sched, work, settle)
}

func (rt *services) settleOwned(settle context.Context) error {
	var err error
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
