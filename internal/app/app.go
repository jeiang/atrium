package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
)

var log = logx.Logger("atrium.app")

type Mode int

const (
	OneShot Mode = iota
	Resident
)

type App struct {
	key       db.Key
	db        *db.DB
	exclusion *db.ServeLock
	env       config.Env
	config    config.Config
	state     *config.State
	catalog   *module.Catalog
	live      *liveRun
	rt        *services
	work      context.Context
	stopWork  context.CancelCauseFunc
	drainMu   sync.Mutex
	drains    []func(context.Context) error
	draining  atomic.Bool
	stopping  *shutdown
	watchdog  func()
}

func Open(ctx context.Context, env config.Env, cat *module.Catalog, mode Mode) (*App, error) {
	cfg, err := config.Load(env)
	if err != nil {
		return nil, &refusal{err}
	}
	st := config.From(ctx)
	k := st.DBKey()
	var exclusion *db.ServeLock
	if mode == Resident {
		if exclusion, err = db.TryServeLock(ctx, cfg.DBPath); err != nil {
			return nil, err
		}
	}
	d, err := db.Connect(cfg.DBPath, db.WithKey(k))
	if err != nil {
		return nil, errors.Join(err, exclusion.Close())
	}
	if err := st.Boot(ctx, env, d, cfg); err != nil {
		return nil, errors.Join(err, d.Close(), exclusion.Close())
	}
	if err := exclusion.Check(); err != nil {
		return nil, errors.Join(err, d.Close(), exclusion.Close())
	}
	return &App{key: k, db: d, exclusion: exclusion, env: env, config: cfg, state: st, catalog: cat}, nil
}

func (a *App) Key() db.Key { return a.key }

func (a *App) DB() *db.DB { return a.db }

func (a *App) Config() config.Config { return a.config }

func (a *App) State() *config.State { return a.state }

func (a *App) Catalog() *module.Catalog { return a.catalog }

func (a *App) Close() error {
	sd := a.stopping
	if sd == nil {
		sd = newShutdown()
	}
	defer sd.release()
	err := a.drainWork(sd)
	if a.live != nil {
		err = errors.Join(err, a.live.drainStore(sd.settle))
	}
	if a.stopping != nil {
		err = errors.Join(err, checkpoint(sd.settle, a.db))
	}
	err = errors.Join(err, a.db.Close())
	if a.watchdog != nil {
		a.watchdog()
	}
	return errors.Join(err, a.exclusion.Close())
}
