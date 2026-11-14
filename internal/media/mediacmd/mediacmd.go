package mediacmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/cli"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
)

type Job func(ctx context.Context, svc *mediaapp.Service) (any, error)

type Runner func(ctx context.Context, job string, run Job) (any, error)

type Options struct {
	Run      Runner
	Inspect  func(ctx context.Context, id int64) (any, error)
	PollJob  string
	SweepJob string
}

func Poll(ctx context.Context, svc *mediaapp.Service) (any, error) {
	polled, err := svc.Poll(ctx, mediaapp.PollInput{SkipWithoutChannels: true})
	if err != nil {
		return polled.Data.Result.Dict(), err
	}
	if !polled.Data.Ran {
		return map[string]any{}, nil
	}
	return polled.Data.Result.Dict(), nil
}

func Sweep(ctx context.Context, svc *mediaapp.Service) (any, error) {
	swept, err := svc.Sweep(ctx, mediaapp.SweepInput{})
	if err != nil {
		return map[string]any{"selected": anys(swept.Data.Selected), "removed": anys(swept.Data.Removed), "errors": anys(swept.Data.Errors)}, err
	}
	return map[string]any{"selected": anys(swept.Data.Selected), "removed": anys(swept.Data.Removed), "errors": anys(swept.Data.Errors)}, nil
}

func anys(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func Command(o Options) *cli.Command {
	return &cli.Command{
		Name: "media",
		Commands: []*cli.Command{
			{Name: "poll", Help: "poll EZTV for every channel now", Setup: handler(o.Run, o.PollJob, Poll)},
			{Name: "sweep", Help: "select files in new media torrents and remove filed torrents qBittorrent stopped", Setup: handler(o.Run, o.SweepJob, Sweep)},
			{Name: "inspect-filing", Help: "show the media agent receipt and current file evidence for a filing effect", Setup: inspectFiling(o.Inspect)},
		},
	}
}

func handler(run Runner, name string, fn Job) func(*cli.Args) cli.Handler {
	return func(*cli.Args) cli.Handler {
		return func(env *cli.Env) int {
			out, err := run(env.Ctx, name, fn)
			if err != nil {
				if out != nil {
					body, _ := json.MarshalIndent(out, "", "  ")
					_, _ = fmt.Fprintln(env.Stdout, string(body))
				}
				return cli.Fail(env, err)
			}
			body, _ := json.MarshalIndent(out, "", "  ")
			_, _ = fmt.Fprintln(env.Stdout, string(body))
			return cli.ExitOK
		}
	}
}

func inspectFiling(inspect func(ctx context.Context, id int64) (any, error)) func(*cli.Args) cli.Handler {
	return func(a *cli.Args) cli.Handler {
		id := a.IntArg("effect-id", cli.Opt{Help: "the filing effect to inspect"})
		return func(env *cli.Env) int {
			out, err := inspect(env.Ctx, int64(*id))
			if err != nil {
				return cli.Fail(env, err)
			}
			body, _ := json.MarshalIndent(out, "", "  ")
			_, _ = fmt.Fprintln(env.Stdout, string(body))
			return cli.ExitOK
		}
	}
}
