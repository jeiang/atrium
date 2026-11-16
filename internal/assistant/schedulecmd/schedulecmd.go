package schedulecmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/jsonx"
)

type Open func(context.Context) (*asstapp.Service, func(), error)

func Command(open Open) *cli.Command {
	return &cli.Command{
		Name: "schedule",
		Help: "list, create, edit, delete and run scheduled jobs",
		Commands: []*cli.Command{
			listCommand(open), createCommand(open), editCommand(open), deleteCommand(open), runCommand(open),
		},
	}
}

func withService(env *cli.Env, open Open, act func(*asstapp.Service) (any, error)) int {
	svc, done, err := open(env.Ctx)
	if err != nil {
		return cli.Fail(env, err)
	}
	defer done()
	out, err := act(svc)
	if e, ok := errors.AsType[*application.Error](err); ok && e.Code == application.Unavailable {
		_, _ = fmt.Fprintf(env.Stderr, "error: %s\n", e.Message)
		return cli.ExitUnavailable
	}
	if err != nil {
		return cli.Fail(env, err)
	}
	if _, err := fmt.Fprintln(env.Stdout, jsonx.Dumps(out, jsonx.Opts{Indent: 2})); err != nil {
		return cli.Fail(env, err)
	}
	return cli.ExitOK
}

func listCommand(open Open) *cli.Command {
	return &cli.Command{
		Name: "list",
		Help: "list every schedule with its source, next run and last run",
		Setup: func(*cli.Args) cli.Handler {
			return func(env *cli.Env) int {
				return withService(env, open, func(svc *asstapp.Service) (any, error) {
					read, err := svc.ScheduleList(env.Ctx, asstapp.ScheduleListInput{})
					return read.Data, err
				})
			}
		},
	}
}

func createCommand(open Open) *cli.Command {
	return &cli.Command{
		Name: "create",
		Help: "create an operator schedule that runs a prompt on a cron schedule",
		Setup: func(a *cli.Args) cli.Handler {
			name := a.Arg("name", cli.Opt{})
			schedule := a.String("schedule", "", cli.Opt{Help: "cron expression or phrase, such as \"daily 09:00\""})
			prompt := a.String("prompt", "", cli.Opt{Help: "what the assistant does on each run"})
			return func(env *cli.Env) int {
				return withService(env, open, func(svc *asstapp.Service) (any, error) {
					changed, err := svc.ScheduleAdd(env.Ctx, asstapp.ScheduleAddInput{Name: *name, Prompt: *prompt, Schedule: *schedule})
					return changed.Data, err
				})
			}
		},
	}
}

func editCommand(open Open) *cli.Command {
	return &cli.Command{
		Name: "edit",
		Help: "change the schedule, prompt, name or enabled state of a schedule; omitted fields keep their values",
		Setup: func(a *cli.Args) cli.Handler {
			id := a.IntArg("id", cli.Opt{})
			name := a.String("name", "", cli.Opt{})
			prompt := a.String("prompt", "", cli.Opt{})
			schedule := a.String("schedule", "", cli.Opt{})
			enabled := a.String("enabled", "", cli.Opt{Choices: []string{"true", "false"}})
			ifMatch := a.String("if-match", "", cli.Opt{Help: "refuse the change unless the schedule still has this revision"})
			return func(env *cli.Env) int {
				in := asstapp.ScheduleUpdateInput{ID: int64(*id), IfMatch: *ifMatch}
				if a.Has("name") {
					in.Name = name
				}
				if a.Has("prompt") {
					in.Prompt = prompt
				}
				if a.Has("schedule") {
					in.Schedule = schedule
				}
				if a.Has("enabled") {
					on := *enabled == "true"
					in.Enabled = &on
				}
				return withService(env, open, func(svc *asstapp.Service) (any, error) {
					changed, err := svc.ScheduleUpdate(env.Ctx, in)
					return changed.Data, err
				})
			}
		},
	}
}

func deleteCommand(open Open) *cli.Command {
	return &cli.Command{
		Name: "delete",
		Help: "delete a schedule; built-in schedules cannot be deleted",
		Setup: func(a *cli.Args) cli.Handler {
			id := a.IntArg("id", cli.Opt{})
			return func(env *cli.Env) int {
				return withService(env, open, func(svc *asstapp.Service) (any, error) {
					changed, err := svc.ScheduleCancel(env.Ctx, asstapp.ScheduleCancelInput{ID: int64(*id)})
					return changed.Data, err
				})
			}
		},
	}
}

func runCommand(open Open) *cli.Command {
	return &cli.Command{
		Name: "run",
		Help: "ask the running serve to run a schedule now; prints the requested run without waiting",
		Setup: func(a *cli.Args) cli.Handler {
			id := a.IntArg("id", cli.Opt{})
			return func(env *cli.Env) int {
				return withService(env, open, func(svc *asstapp.Service) (any, error) {
					started, err := svc.ScheduleRunNow(env.Ctx, asstapp.ScheduleRunNowInput{ID: int64(*id)})
					return started.Data, err
				})
			}
		},
	}
}
