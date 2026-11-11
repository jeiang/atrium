package agentcmd

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
)

type subagentRow struct {
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Builtin bool     `json:"builtin"`
	Effort  string   `json:"effort"`
	Tools   []string `json:"tools"`
}

func rowOf(a subagents.Agent) subagentRow {
	return subagentRow{Name: a.Name, Enabled: a.Enabled, Builtin: a.Builtin, Effort: a.Effort, Tools: a.Tools}
}

func subagentsCommand(cfgEnv config.Env, tools registry.Source, now func() float64) *cli.Command {
	return &cli.Command{Name: "subagents", Commands: []*cli.Command{
		subagentsList(cfgEnv),
		subagentsSet(cfgEnv, tools, now),
		subagentsToggle(cfgEnv, "enable", true, now),
		subagentsToggle(cfgEnv, "disable", false, now),
		subagentsDelete(cfgEnv),
	}}
}

func openDB(ctx context.Context, cfgEnv config.Env) (*db.DB, error) {
	cfg, err := config.Load(cfgEnv)
	if err != nil {
		return nil, err
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if err := config.Boot(ctx, cfgEnv, d, cfg); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

func withDB(env *cli.Env, cfgEnv config.Env, act func(*db.DB) error) int {
	d, err := openDB(env.Ctx, cfgEnv)
	if err != nil {
		return cli.Fail(env, err)
	}
	defer func() { _ = d.Close() }()
	if err := act(d); err != nil {
		return cli.Fail(env, err)
	}
	return cli.ExitOK
}

func stamp(now func() float64) float64 { return clock(now)() }

func unknown(name string) error { return fmt.Errorf("no subagent %q", name) }

func subagentsList(cfgEnv config.Env) *cli.Command {
	return &cli.Command{
		Name: "list",
		Help: "list subagent definitions",
		Setup: func(*cli.Args) cli.Handler {
			return func(env *cli.Env) int {
				return withDB(env, cfgEnv, func(d *db.DB) error {
					found, err := subagents.List(env.Ctx, d.Read)
					if err != nil {
						return err
					}
					rows := make([]subagentRow, 0, len(found))
					for _, a := range found {
						rows = append(rows, rowOf(a))
					}
					_, err = fmt.Fprintln(env.Stdout, jsonx.Dumps(rows, jsonx.Opts{Indent: 2}))
					return err
				})
			}
		},
	}
}

func subagentsSet(cfgEnv config.Env, tools registry.Source, now func() float64) *cli.Command {
	return &cli.Command{
		Name: "set",
		Help: "create or update a subagent; omitted fields keep their stored values",
		Setup: func(args *cli.Args) cli.Handler {
			name := args.Arg("name", cli.Opt{})
			description := args.String("description", "", cli.Opt{})
			prompt := args.String("prompt", "", cli.Opt{})
			promptFile := args.String("prompt-file", "", cli.Opt{Metavar: "PATH"})
			effort := args.String("effort", pricing.Medium, cli.Opt{Choices: pricing.Efforts})
			toolNames := args.Repeated("tool", cli.Opt{Help: "read-only tool open to subagents; repeat for each"})
			return func(env *cli.Env) int {
				if args.Has("prompt") && args.Has("prompt-file") {
					return env.UsageError("--prompt and --prompt-file are exclusive")
				}
				text := *prompt
				if args.Has("prompt-file") {
					raw, err := os.ReadFile(*promptFile)
					if err != nil {
						return cli.Fail(env, err)
					}
					text = string(raw)
				}
				reg, err := tools()
				if err != nil {
					return cli.Fail(env, err)
				}
				return withDB(env, cfgEnv, func(d *db.DB) error {
					var saved *subagents.Agent
					err := d.WithTx(env.Ctx, func(tx *sql.Tx) error {
						existing, err := subagents.Get(env.Ctx, tx, *name)
						if err != nil {
							return err
						}
						def := subagents.Definition{Name: *name, Description: *description, Prompt: text, Effort: *effort, Tools: *toolNames, Enabled: true}
						if existing != nil {
							def.Enabled = existing.Enabled
							if !args.Has("description") {
								def.Description = existing.Description
							}
							if !args.Has("prompt") && !args.Has("prompt-file") {
								def.Prompt = existing.Prompt
							}
							if !args.Has("effort") {
								def.Effort = existing.Effort
							}
							if !args.Has("tool") {
								def.Tools = existing.Tools
							}
						}
						saved, err = subagents.Save(env.Ctx, tx, reg, def, stamp(now))
						return err
					})
					if err != nil {
						return err
					}
					_, err = fmt.Fprintln(env.Stdout, jsonx.Dumps(rowOf(*saved), jsonx.Opts{Indent: 2}))
					return err
				})
			}
		},
	}
}

func subagentsToggle(cfgEnv config.Env, verb string, enabled bool, now func() float64) *cli.Command {
	return &cli.Command{
		Name: verb,
		Help: verb + " a subagent",
		Setup: func(args *cli.Args) cli.Handler {
			name := args.Arg("name", cli.Opt{})
			return func(env *cli.Env) int {
				return withDB(env, cfgEnv, func(d *db.DB) error {
					changed, err := subagents.SetEnabled(env.Ctx, d.Write, *name, enabled, stamp(now))
					if err != nil {
						return err
					}
					if !changed {
						return unknown(*name)
					}
					return nil
				})
			}
		},
	}
}

func subagentsDelete(cfgEnv config.Env) *cli.Command {
	return &cli.Command{
		Name: "delete",
		Help: "delete a subagent; built-in agents can only be disabled",
		Setup: func(args *cli.Args) cli.Handler {
			name := args.Arg("name", cli.Opt{})
			return func(env *cli.Env) int {
				return withDB(env, cfgEnv, func(d *db.DB) error {
					removed, err := subagents.Delete(env.Ctx, d.Write, *name)
					if err != nil {
						return err
					}
					if !removed {
						return unknown(*name)
					}
					return nil
				})
			}
		},
	}
}
