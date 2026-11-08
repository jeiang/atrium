package main

import (
	"context"
	"io"

	"github.com/jeiang/atrium/internal/config"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/settings"
)

func bootstrap(env config.Env, reg *settings.Registry) func(context.Context, io.Writer) error {
	return func(_ context.Context, _ io.Writer) error {
		pins, err := config.LoadPins(env, reg)
		if err != nil {
			return err
		}
		reg.SetPins(pins)
		return dbkey.Configure(env, reg)
	}
}
