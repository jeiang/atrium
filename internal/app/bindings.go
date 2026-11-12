package app

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/web"
)

const (
	workDrainTimeout   = 15 * time.Second
	ledgerDrainTimeout = 5 * time.Second
)

var (
	errUnbound = errors.New("the assistant live-run service is not open in this process")
)

type Bindings struct {
	env   config.Env
	tools registry.Source
	now   func() float64
	live  atomic.Pointer[App]
}

func NewBindings(env config.Env, tools registry.Source, now func() float64) *Bindings {
	return &Bindings{env: env, tools: tools, now: now}
}

type liveRun struct {
	store   *ledger.Store
	svc     *service.Service
	base    context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	stopped bool
	work    sync.WaitGroup
}

type appKey struct{}

func (b *Bindings) open(ctx context.Context) (*App, error) {
	set := module.SetFrom(ctx)
	if set == nil {
		return nil, module.ErrNotEvaluated
	}
	a, err := Open(ctx, b.env, set.Catalog())
	if err != nil {
		return nil, err
	}
	if b.live.CompareAndSwap(nil, a) {
		a.unbind = func() { b.live.CompareAndSwap(a, nil) }
	}
	return a, nil
}

func (b *Bindings) Assistant(ctx context.Context) (*service.Service, error) {
	if a, ok := ctx.Value(appKey{}).(*App); ok {
		return a.Assistant(), nil
	}
	if b == nil {
		return nil, errUnbound
	}
	a := b.live.Load()
	if a == nil || a.Assistant() == nil {
		return nil, errUnbound
	}
	return a.Assistant(), nil
}

func (b *Bindings) OpenAssistant(ctx context.Context) (context.Context, *service.Service, func(), error) {
	if b == nil {
		return nil, nil, nil, errUnbound
	}
	a, err := b.open(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := a.bindLive(ctx, b.tools, b.now); err != nil {
		return nil, nil, nil, errors.Join(err, a.Close())
	}
	return context.WithValue(ctx, appKey{}, a), a.Assistant(), func() {
		if err := a.Close(); err != nil {
			log.Warn("could not drain the application before closing the database", logx.Err, err.Error())
		}
	}, nil
}

func (b *Bindings) ServeBoot() web.Boot { return &serveBoot{b: b} }

type serveBoot struct {
	b *Bindings
}

func (s *serveBoot) Open(ctx context.Context) (context.Context, *db.DB, config.Config, func() error, error) {
	a, err := s.b.open(ctx)
	if err != nil {
		return nil, nil, config.Config{}, nil, err
	}
	return context.WithValue(ctx, appKey{}, a), a.DB(), a.Config(), a.Close, nil
}

func (s *serveBoot) Start(ctx context.Context, d *web.Deps) error {
	a, ok := ctx.Value(appKey{}).(*App)
	if !ok {
		return errUnbound
	}
	set := module.SetFrom(ctx)
	if err := set.Start(ctx, d); err != nil {
		return err
	}
	if err := a.bindLive(ctx, s.b.tools, s.b.now); err != nil {
		return err
	}
	svc := a.Assistant()
	d.ResumeRun = func(ctx context.Context, runID int64) error {
		err := svc.StartResume(ctx, runID)
		var notInterrupted service.NotInterrupted
		if errors.As(err, &notInterrupted) {
			return nil
		}
		return err
	}
	return set.OnEnable(module.Assistant, a.startAssistant)
}

func (a *App) bindLive(ctx context.Context, tools registry.Source, now func() float64) error {
	open := imapclient.Opener(a.config)
	store, err := a.catalog.Ledger(a.db, a.env, open, assistant.Now)
	if err != nil {
		return err
	}
	run, err := runner.New(tools, store, open)
	if err != nil {
		return err
	}
	base, cancel := context.WithCancel(ctx)
	svc := service.New(base, a.db, a.config, a.env, run)
	svc.Now = now
	if svc.Now == nil {
		svc.Now = assistant.Now
	}
	a.live = &liveRun{store: store, svc: svc, base: base, cancel: cancel}
	return nil
}

func (a *App) Assistant() *service.Service {
	if a.live == nil {
		return nil
	}
	return a.live.svc
}

func (a *App) startAssistant() error {
	l := a.live
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return nil
	}
	tz := config.From(l.base).TZName(a.env)
	jobs := a.catalog.Jobs()
	stamp := assistant.Now()
	if err := a.db.WithTx(l.base, func(tx *sql.Tx) error {
		return scheduler.Ensure(l.base, tx, jobs, tz, stamp)
	}); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	l.work.Go(func() { l.svc.CheckCatalog(l.base) })
	return nil
}

func (l *liveRun) drain() error {
	l.mu.Lock()
	l.stopped = true
	l.mu.Unlock()
	work, stopWork := context.WithTimeout(context.Background(), workDrainTimeout)
	defer stopWork()
	err := l.svc.Drain(work)
	l.cancel()
	l.work.Wait()
	final, stopFinal := context.WithTimeout(context.Background(), ledgerDrainTimeout)
	defer stopFinal()
	return errors.Join(err, l.store.Drain(final))
}
