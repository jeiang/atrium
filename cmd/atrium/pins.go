package main

import (
	"context"
	"io"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/settings"
)

func bootstrap(env config.Env, reg *settings.Registry) func(context.Context, io.Writer) error {
	return func(ctx context.Context, _ io.Writer) error {
		host, err := config.LoadHost(env, reg, config.HostFile(env))
		if err != nil {
			return err
		}
		config.SetHostFrom(ctx, host)
		pins, err := config.LoadPins(env, reg)
		if err != nil {
			return err
		}
		reg.SetPins(config.CombinePins(host, pins))
		k, err := dbkey.Configure(env, reg)
		if err != nil {
			return err
		}
		config.From(ctx).SetDBKey(k)
		db.SetDefaultKey(k)
		return nil
	}
}
