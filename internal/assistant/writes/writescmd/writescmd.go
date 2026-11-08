package writescmd

import (
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func Command(env config.Env) *cli.Command {
	return &cli.Command{Name: "writes", Commands: []*cli.Command{resolve(env)}}
}

func resolve(env config.Env) *cli.Command {
	return &cli.Command{
		Name: "resolve",
		Help: "settle an agent write with an unknown outcome after checking the external system",
		Setup: func(a *cli.Args) cli.Handler {
			id := a.IntArg("id", cli.Opt{})
			return func(e *cli.Env) int {
				cfg, err := config.Load(env)
				if err != nil {
					return cli.Fail(e, err)
				}
				d, err := db.Connect(cfg.DBPath)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer func() { _ = d.Close() }()
				now := float64(time.Now().UnixNano()) / 1e9
				if err := writes.Resolve(e.Ctx, d, int64(*id), writes.Resolver{Actor: "operator", Source: "cli"}, func() float64 { return now }); err != nil {
					return cli.Fail(e, err)
				}
				_, _ = fmt.Fprintf(e.Stdout, "{\"id\": %d, \"resolved\": true}\n", *id)
				return cli.ExitOK
			}
		},
	}
}
