package app

import (
	"context"
	"database/sql"
	"errors"
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
)

const (
	workDrainTimeout   = 15 * time.Second
	ledgerDrainTimeout = 5 * time.Second
)

var (
	errUnbound = errors.New("the application is not open in this context")
)

type Bindings struct {
	env   config.Env
	tools registry.Source
	now   func() float64
}

func NewBindings(env config.Env, tools registry.Source, now func() float64) *Bindings {
	return &Bindings{env: env, tools: tools, now: now}
}

type liveRun struct {
	store   *ledger.Store
	svc     *service.Service
	cancel  context.CancelFunc
	checked atomic.Bool
}

type appKey struct{}

func bound(ctx context.Context) (*App, error) {
	if a, ok := ctx.Value(appKey{}).(*App); ok {
		return a, nil
	}
	return nil, errUnbound
}

func (b *Bindings) open(ctx context.Context, mode Mode) (*App, context.Context, error) {
	set := module.SetFrom(ctx)
	if set == nil {
		return nil, nil, module.ErrNotEvaluated
	}
	a, err := Open(ctx, b.env, set.Catalog(), mode)
	if err != nil {
		return nil, nil, err
	}
	return a, context.WithValue(ctx, appKey{}, a), nil
}

func (b *Bindings) Assistant(ctx context.Context) (*service.Service, error) {
	a, err := bound(ctx)
	if err != nil {
		return nil, err
	}
	return a.Assistant(), nil
}

func (b *Bindings) Resources(ctx context.Context) (*db.DB, config.Config, error) {
	a, err := bound(ctx)
	if err != nil {
		return nil, config.Config{}, err
	}
	return a.db, a.config, nil
}

func (b *Bindings) StartAssistant(ctx context.Context) error {
	a, err := bound(ctx)
	if err != nil {
		return err
	}
	if a.live == nil {
		return errUnbound
	}
	return a.startAssistant(ctx)
}

func (b *Bindings) OpenAssistant(ctx context.Context) (context.Context, *service.Service, func(), error) {
	if b == nil {
		return nil, nil, nil, errUnbound
	}
	a, ctx, err := b.open(ctx, OneShot)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := a.bindLive(ctx, b.tools, b.now); err != nil {
		return nil, nil, nil, errors.Join(err, a.Close())
	}
	return ctx, a.Assistant(), func() {
		if err := a.Close(); err != nil {
			log.Warn("could not drain the application before closing the database", logx.Err, err.Error())
		}
	}, nil
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
	a.live = &liveRun{store: store, svc: svc, cancel: cancel}
	return nil
}

func (a *App) Assistant() *service.Service {
	if a.live == nil {
		return nil
	}
	return a.live.svc
}

func (a *App) startAssistant(ctx context.Context) error {
	tz := config.From(ctx).TZName(a.env)
	jobs := a.catalog.Jobs()
	stamp := assistant.Now()
	if err := a.db.WithTx(ctx, func(tx *sql.Tx) error {
		return scheduler.Ensure(ctx, tx, jobs, tz, stamp)
	}); err != nil {
		return err
	}
	if a.live.checked.CompareAndSwap(false, true) {
		a.live.svc.CheckCatalog(ctx)
	}
	return nil
}

func (l *liveRun) drain() error {
	work, stopWork := context.WithTimeout(context.Background(), workDrainTimeout)
	defer stopWork()
	err := l.svc.Drain(work)
	l.cancel()
	return err
}

func (l *liveRun) drainStore() error {
	final, stopFinal := context.WithTimeout(context.Background(), ledgerDrainTimeout)
	defer stopFinal()
	return l.store.Drain(final)
}
