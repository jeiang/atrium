package app

import (
	"context"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
)

type App struct {
	key     db.Key
	db      *db.DB
	config  config.Config
	state   *config.State
	catalog *module.Catalog
}

func Open(ctx context.Context, env config.Env, cat *module.Catalog) (*App, error) {
	cfg, err := config.Load(env)
	if err != nil {
		return nil, err
	}
	st := config.From(ctx)
	k := st.DBKey()
	d, err := db.Connect(cfg.DBPath, db.WithKey(k))
	if err != nil {
		return nil, err
	}
	if err := st.Boot(ctx, env, d, cfg); err != nil {
		_ = d.Close()
		return nil, err
	}
	return &App{key: k, db: d, config: cfg, state: st, catalog: cat}, nil
}

func (a *App) Key() db.Key { return a.key }

func (a *App) DB() *db.DB { return a.db }

func (a *App) Config() config.Config { return a.config }

func (a *App) State() *config.State { return a.state }

func (a *App) Catalog() *module.Catalog { return a.catalog }

func (a *App) Close() error { return a.db.Close() }
