package calendarmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/apps/calendar"
	calapp "github.com/jeiang/atrium/internal/apps/calendar/application"
	"github.com/jeiang/atrium/internal/apps/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func calendarCLI(env config.Env) calendarcmd.Open {
	return func(ctx context.Context) (*calapp.Service, config.Config, func(), error) {
		cfg, err := config.Load(env)
		if err != nil {
			return nil, cfg, nil, err
		}
		d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(ctx).DBKey()))
		if err != nil {
			return nil, cfg, nil, err
		}
		if err := config.Boot(ctx, env, d, cfg); err != nil {
			_ = d.Close()
			return nil, cfg, nil, err
		}
		return calapp.New(d, env, nil, calendar.Now), cfg, func() { _ = d.Close() }, nil
	}
}
