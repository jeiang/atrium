package kbcmd

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/settings"
)

func Command(env config.Env) *cli.Command {
	return &cli.Command{Name: "kb", Commands: []*cli.Command{importCommand(env)}}
}

func importCommand(env config.Env) *cli.Command {
	return &cli.Command{
		Name: "import",
		Help: "copy the profile, nightly observations and Notion wiki pages into the knowledge store, idempotent by origin",
		Setup: func(a *cli.Args) cli.Handler {
			dryRun := a.Bool("dry-run", cli.Opt{Help: "report what would change without writing"})
			return func(e *cli.Env) int {
				cfg, err := config.Load(env)
				if err != nil {
					return cli.Fail(e, err)
				}
				key, err := dbkey.Load(env, settings.From(e.Ctx))
				if err != nil {
					return cli.Fail(e, err)
				}
				d, err := db.Connect(cfg.DBPath, db.WithKey(key))
				if err != nil {
					return cli.Fail(e, err)
				}
				defer func() { _ = d.Close() }()
				now := float64(time.Now().UnixNano()) / 1e9
				stats, err := kb.Import(e.Ctx, d, *dryRun, now)
				if err != nil {
					return cli.Fail(e, err)
				}
				raw, err := json.Marshal(stats)
				if err != nil {
					return cli.Fail(e, err)
				}
				_, _ = fmt.Fprintf(e.Stdout, "%s\n", raw)
				if len(stats.FailedItems) > 0 {
					return cli.ExitError
				}
				return cli.ExitOK
			}
		},
	}
}
