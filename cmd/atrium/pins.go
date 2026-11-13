package main

import (
	"context"
	"errors"
	"io"
	"io/fs"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/settings"
)

func bootstrap(env config.Env, reg *settings.Registry) func(context.Context, io.Writer) error {
	return func(ctx context.Context, _ io.Writer) error {
		host, err := config.LoadHost(env, reg, config.HostFile(env))
		if err != nil {
			return refusal(err)
		}
		config.SetHostFrom(ctx, host)
		pins, err := config.LoadPins(env, reg)
		if err != nil {
			return refusal(err)
		}
		reg.SetPins(config.CombinePins(host, pins))
		k, err := dbkey.Configure(env, reg)
		if err != nil {
			return refusal(err)
		}
		config.From(ctx).SetDBKey(k)
		return nil
	}
}

func refusal(err error) error {
	if errors.As(err, new(*fs.PathError)) {
		return err
	}
	return &cli.ConfigRefusal{Err: err}
}
