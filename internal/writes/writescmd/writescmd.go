package writescmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
)

type Options struct {
	Now  func() float64
	Open func(context.Context) (*reviewapp.Service, func(), error)
}

func (o Options) now() float64 {
	if o.Now != nil {
		return o.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func Command(o Options) *cli.Command {
	return &cli.Command{
		Name: "writes",
		Help: "list, resolve and undo direct writes in the effect ledger",
		Commands: []*cli.Command{
			list(o),
			resolve(o),
			undo(o),
		},
	}
}

func list(o Options) *cli.Command {
	return &cli.Command{
		Name: "list",
		Help: "list direct writes, newest first",
		Setup: func(a *cli.Args) cli.Handler {
			origin := a.String("origin", "", cli.Opt{Help: "filter by origin"})
			operation := a.String("operation", "", cli.Opt{Help: "filter by operation"})
			limit := a.Int("limit", 50, cli.Opt{Help: "maximum writes"})
			blocked := a.Bool("blocked", cli.Opt{Help: "only unresolved writes"})
			return func(e *cli.Env) int {
				svc, done, err := o.Open(e.Ctx)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer done()
				if err := svc.RecoverStuck(e.Ctx, o.now()); err != nil {
					return cli.Fail(e, err)
				}
				f := reviewapp.EffectsInput{Origins: reviewapp.DirectOrigins, Operation: *operation, Limit: *limit, BlockedOnly: *blocked}
				if *origin != "" {
					f.Origins = []string{*origin}
				}
				found, err := svc.Effects(e.Ctx, f)
				if err != nil {
					return cli.Fail(e, err)
				}
				return write(e, found.Data)
			}
		},
	}
}

func resolve(o Options) *cli.Command {
	return &cli.Command{
		Name: "resolve",
		Help: "record what an unknown, partial or expired write actually did",
		Setup: func(a *cli.Args) cli.Handler {
			id := a.IntArg("id", cli.Opt{})
			outcome := a.String("outcome", "", cli.Opt{Required: true, Choices: []string{"applied", "not-applied", "partial"},
				Help: "applied, not-applied or partial"})
			evidence := a.String("evidence", "", cli.Opt{Help: "JSON evidence recorded with the resolution"})
			return func(e *cli.Env) int {
				svc, done, err := o.Open(e.Ctx)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer done()
				if err := svc.RecoverStuck(e.Ctx, o.now()); err != nil {
					return cli.Fail(e, err)
				}
				var raw json.RawMessage
				if *evidence != "" {
					raw = json.RawMessage(*evidence)
				}
				found, err := svc.Resolve(e.Ctx, reviewapp.ResolveInput{Target: reviewapp.Target{Kind: reviewapp.EffectTarget, ID: int64(*id)}, Outcome: *outcome, Evidence: raw})
				if err != nil {
					return cli.Fail(e, err)
				}
				return write(e, found.Data.Effect)
			}
		},
	}
}

func undo(o Options) *cli.Command {
	return &cli.Command{
		Name: "undo",
		Help: "undo a completed or partial write",
		Setup: func(a *cli.Args) cli.Handler {
			id := a.IntArg("id", cli.Opt{})
			return func(e *cli.Env) int {
				svc, done, err := o.Open(e.Ctx)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer done()
				if err := svc.RecoverStuck(e.Ctx, o.now()); err != nil {
					return cli.Fail(e, err)
				}
				found, err := svc.Undo(e.Ctx, reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.EffectTarget, ID: int64(*id)}})
				if err != nil {
					return cli.Fail(e, err)
				}
				return write(e, found.Data.Undo)
			}
		},
	}
}

func write(e *cli.Env, value any) int {
	enc := json.NewEncoder(e.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		_, _ = fmt.Fprintln(e.Stderr, "error:", err)
		return cli.ExitError
	}
	return cli.ExitOK
}
