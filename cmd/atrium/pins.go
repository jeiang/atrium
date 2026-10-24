package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings"
)

var log = logx.Logger("atrium.config")

func pinned(cmds []*cli.Command) []*cli.Command {
	for _, c := range cmds {
		if c.Setup != nil {
			setup := c.Setup
			c.Setup = func(a *cli.Args) cli.Handler {
				run := setup(a)
				return func(e *cli.Env) int {
					pins, err := config.LoadPins(config.OS, settings.Default)
					if err != nil {
						_, _ = fmt.Fprintf(e.Stderr, "error: %v\n", err)
						return cli.ExitError
					}
					settings.Default.SetPins(pins)
					if err := dbkey.Configure(config.OS, settings.Default); err != nil {
						_, _ = fmt.Fprintf(e.Stderr, "error: %v\n", err)
						return cli.ExitError
					}
					warnDropped(config.OS)
					warnOrphans(e.Ctx, config.OS)
					return run(e)
				}
			}
		}
		pinned(c.Commands)
	}
	return cmds
}

func warnDropped(env config.Env) {
	for _, d := range config.DroppedEnv(env, settings.Default) {
		if d.New == "" {
			log.Warn("environment variable is no longer read and is ignored", logx.EnvName, d.Old)
			continue
		}
		log.Warn("environment variable is no longer read and is ignored", logx.EnvName, d.Old, logx.UseInstead, d.New)
	}
}

func warnOrphans(ctx context.Context, env config.Env) {
	cfg, err := config.Load(env)
	if err != nil {
		return
	}
	if _, err := os.Stat(cfg.DBPath); err != nil {
		return
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return
	}
	defer func() { _ = d.Close() }()
	orphans, err := settings.Default.OrphansIfPresent(ctx, d.Read)
	if err != nil {
		return
	}
	for _, o := range orphans {
		log.Warn("stored setting is retired and ignored; remove it with atrium settings unset", logx.SettingKey, o.Key, logx.SettingValue, o.Value, logx.UseInstead, o.Replacement)
	}
}
