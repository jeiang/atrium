package config

import (
	"context"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

type state struct {
	core         core
	fileAccounts []AccountSpec
	accounts     []AccountSpec
}

var current atomic.Pointer[state]

func loadState() *state {
	if s := current.Load(); s != nil {
		return s
	}
	return &state{}
}

func OpenStore(ctx context.Context, env Env) (*db.DB, *settings.Store, error) {
	cfg, err := Load(env)
	if err != nil {
		return nil, nil, err
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	if err := Boot(ctx, env, d, cfg); err != nil {
		_ = d.Close()
		return nil, nil, err
	}
	return d, settings.NewStore(settings.Default, d, d), nil
}

func Boot(ctx context.Context, env Env, d *db.DB, cfg Config) error {
	if settings.Default.PinSource() == nil {
		pins, err := LoadPins(env, settings.Default)
		if err != nil {
			return err
		}
		settings.Default.SetPins(pins)
	}
	if err := db.Migrate(ctx, d, cfg.DBPath); err != nil {
		return err
	}
	c, err := loadCore(ctx, d.Read)
	if err != nil {
		return err
	}
	files, err := loadAccounts(env)
	if err != nil {
		return err
	}
	accounts, err := mergeStoredAccounts(ctx, d.Read, files)
	if err != nil {
		return err
	}
	current.Store(&state{core: c, fileAccounts: files, accounts: accounts})
	return nil
}
