package attentioncmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
)

func Command(current func() *module.Snapshot) *cli.Command {
	return &cli.Command{
		Name: "attention",
		Help: "list what needs the operator's attention",
		Commands: []*cli.Command{
			{Name: "list", Help: "print the ordered attention queue", Setup: func(a *cli.Args) cli.Handler {
				at := a.String("at", "", cli.Opt{Help: "RFC 3339 time to evaluate at, default now", Metavar: "TIME"})
				asJSON := a.Bool("json", cli.Opt{Help: "print JSON"})
				return func(e *cli.Env) int {
					now := time.Now()
					if *at != "" {
						var err error
						if now, err = time.Parse(time.RFC3339, *at); err != nil {
							return e.UsageError("--at must be an RFC 3339 time")
						}
					}
					return withDB(e, func(d *db.DB, loc *time.Location) int {
						res, err := attention.Query(e.Ctx, d.Read, current().Attention(), now, loc)
						if err != nil {
							return cli.Fail(e, err)
						}
						if *asJSON {
							enc := json.NewEncoder(e.Stdout)
							enc.SetIndent("", "  ")
							if err := enc.Encode(res); err != nil {
								return cli.Fail(e, err)
							}
							return cli.ExitOK
						}
						printText(e, res)
						return cli.ExitOK
					})
				}
			}},
			{Name: "snooze", Help: "hide an attention item until a date", Setup: func(a *cli.Args) cli.Handler {
				kind := a.Arg("kind", cli.Opt{Help: "item kind"})
				id := a.Arg("source-id", cli.Opt{Help: "item source id"})
				until := a.String("until", "", cli.Opt{Help: "first day the item shows again, YYYY-MM-DD", Metavar: "DATE", Required: true})
				return func(e *cli.Env) int {
					return withDB(e, func(d *db.DB, loc *time.Location) int {
						when, err := time.ParseInLocation("2006-01-02", *until, loc)
						if err != nil {
							return e.UsageError("--until must be a date as YYYY-MM-DD")
						}
						if err := attention.Snooze(e.Ctx, d.Write, *kind, *id, when, time.Now()); err != nil {
							return e.UsageError("%v", err)
						}
						return cli.ExitOK
					})
				}
			}},
		},
	}
}

func withDB(e *cli.Env, run func(*db.DB, *time.Location) int) int {
	cfg, err := config.Load(config.OS)
	if err != nil {
		return cli.Fail(e, err)
	}
	loc, err := config.Location(config.OS)
	if err != nil {
		return cli.Fail(e, err)
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return cli.Fail(e, err)
	}
	defer func() { _ = d.Close() }()
	if err := config.Migrate(e.Ctx, d, cfg); err != nil {
		return cli.Fail(e, err)
	}
	return run(d, loc)
}

func printText(e *cli.Env, res attention.Result) {
	section := func(title string, items []attention.Item) {
		if len(items) == 0 {
			return
		}
		if title != "" {
			_, _ = fmt.Fprintf(e.Stdout, "%s\n", title)
		}
		for _, it := range items {
			line := fmt.Sprintf("%s\t%s/%s\t%s", it.Class, it.Kind, it.SourceID, it.Title)
			if it.Due != "" {
				line += "\tdue " + it.Due
			}
			var labels []string
			for _, a := range it.Actions {
				labels = append(labels, a.Label)
			}
			if len(labels) > 0 {
				line += "\t[" + strings.Join(labels, ", ") + "]"
			}
			_, _ = fmt.Fprintln(e.Stdout, line)
		}
	}
	section("", res.Items)
	section("Waiting", res.Waiting)
	section("Upcoming", res.Upcoming)
}
