package agentcmd

import (
	"context"
	"fmt"
	"os"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/jsonx"
)

type OpenService func(context.Context) (*asstapp.Service, func(), error)

func subagentsCommand(open OpenService) *cli.Command {
	return &cli.Command{Name: "subagents", Commands: []*cli.Command{
		subagentsList(open),
		subagentsSet(open),
		subagentsToggle(open, "enable", true),
		subagentsToggle(open, "disable", false),
		subagentsDelete(open),
	}}
}

func withService(env *cli.Env, open OpenService, act func(*asstapp.Service) error) int {
	svc, done, err := open(env.Ctx)
	if err != nil {
		return cli.Fail(env, err)
	}
	defer done()
	if err := act(svc); err != nil {
		return cli.Fail(env, err)
	}
	return cli.ExitOK
}

func printJSON(env *cli.Env, v any) error {
	_, err := fmt.Fprintln(env.Stdout, jsonx.Dumps(v, jsonx.Opts{Indent: 2}))
	return err
}

func subagentsList(open OpenService) *cli.Command {
	return &cli.Command{
		Name: "list",
		Help: "list subagent definitions",
		Setup: func(*cli.Args) cli.Handler {
			return func(env *cli.Env) int {
				return withService(env, open, func(svc *asstapp.Service) error {
					found, err := svc.SubagentList(env.Ctx, asstapp.SubagentListInput{})
					if err != nil {
						return err
					}
					return printJSON(env, found.Data)
				})
			}
		},
	}
}

func subagentsSet(open OpenService) *cli.Command {
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
				in := asstapp.SubagentSetInput{Name: *name}
				if args.Has("description") {
					in.Description = description
				}
				if args.Has("prompt") {
					in.Prompt = prompt
				}
				if args.Has("prompt-file") {
					raw, err := os.ReadFile(*promptFile)
					if err != nil {
						return cli.Fail(env, err)
					}
					text := string(raw)
					in.Prompt = &text
				}
				if args.Has("effort") {
					in.Effort = effort
				}
				if args.Has("tool") {
					in.Tools = append([]string{}, *toolNames...)
				}
				return withService(env, open, func(svc *asstapp.Service) error {
					saved, err := svc.SubagentSet(env.Ctx, in)
					if err != nil {
						return err
					}
					return printJSON(env, saved.Data)
				})
			}
		},
	}
}

func subagentsToggle(open OpenService, verb string, enabled bool) *cli.Command {
	return &cli.Command{
		Name: verb,
		Help: verb + " a subagent",
		Setup: func(args *cli.Args) cli.Handler {
			name := args.Arg("name", cli.Opt{})
			return func(env *cli.Env) int {
				return withService(env, open, func(svc *asstapp.Service) error {
					_, err := svc.SubagentEnabled(env.Ctx, asstapp.SubagentEnabledInput{Name: *name, Enabled: enabled})
					return err
				})
			}
		},
	}
}

func subagentsDelete(open OpenService) *cli.Command {
	return &cli.Command{
		Name: "delete",
		Help: "delete a subagent; built-in agents can only be disabled",
		Setup: func(args *cli.Args) cli.Handler {
			name := args.Arg("name", cli.Opt{})
			return func(env *cli.Env) int {
				return withService(env, open, func(svc *asstapp.Service) error {
					_, err := svc.SubagentDelete(env.Ctx, asstapp.SubagentDeleteInput{Name: *name})
					return err
				})
			}
		},
	}
}
