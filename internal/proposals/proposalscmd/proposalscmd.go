package proposalscmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	mailapp "github.com/jeiang/atrium/internal/mail/application"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/jsonx"
	proposalsapp "github.com/jeiang/atrium/internal/proposals/application"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
)

func nullable[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}

type Options struct {
	Now     func() float64
	Session func(context.Context, func(Runtime) error) error
}

type Runtime struct {
	Reads   *reviewapp.Service
	Review  func() *reviewapp.Service
	Scanner func(func() float64) *mailapp.Service
	Namer   func(context.Context) (mailapp.Namer, error)
}

func (o Options) now() float64 {
	if o.Now != nil {
		return o.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

type exitError struct {
	code int
	text string
}

func (e *exitError) Error() string { return e.text }

func failure(code int, err error) error { return &exitError{code: code, text: err.Error()} }

type exec struct {
	Runtime
	Ctx    context.Context
	Stdout io.Writer
	Stderr io.Writer
	opts   Options
}

func (x *exec) Now() float64 { return x.opts.now() }

type dismissView struct {
	ID        int64 `json:"id"`
	Dismissed bool  `json:"dismissed"`
}

func (x *exec) JSON(value any) {
	switch value.(type) {
	case proposalsapp.ProposalView, []reviewapp.ProposalSummary, dismissView:
		enc := json.NewEncoder(x.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(value); err != nil {
			_, _ = fmt.Fprintln(x.Stderr, "error:", err)
		}
		return
	}
	_, _ = fmt.Fprintln(x.Stdout, jsonx.Dumps(value, jsonx.Opts{Indent: 2}))
}

func selectionOf(raw string) (any, error) {
	parsed, err := jsonx.Loads([]byte(raw))
	if err != nil {
		if strings.TrimSpace(raw) == "{" {
			return nil, &application.Error{Code: application.Invalid, Message: "Expecting property name enclosed in double quotes: line 1 column 2 (char 1)"}
		}
		return nil, &application.Error{Code: application.Invalid, Message: err.Error()}
	}
	b, err := json.Marshal(parsed)
	if err != nil {
		return nil, err
	}
	var chosen any
	if err := json.Unmarshal(b, &chosen); err != nil {
		return nil, err
	}
	return chosen, nil
}

func recorded(err error, unknown string) error {
	var e *application.Error
	switch {
	case !errors.As(err, &e):
		return err
	case e.Code == application.NotFound:
		return errors.New(unknown)
	case e.Code == application.Invalid && len(e.Fields) == 1 && e.Fields[0].Name == "selection":
		return &application.Error{Code: application.Invalid, Message: e.Message}
	}
	return err
}

type body func(x *exec) error

func (o Options) leaf(name string, declare func(a *cli.Args) body) *cli.Command {
	return &cli.Command{
		Name: name,
		Setup: func(a *cli.Args) cli.Handler {
			run := declare(a)
			return func(env *cli.Env) int { return o.execute(env, run) }
		},
	}
}

func Command(o Options) *cli.Command {
	return &cli.Command{Name: "proposals", Commands: []*cli.Command{
		o.leaf("scan-all", func(*cli.Args) body {
			return func(x *exec) error {
				scanner := x.Scanner(x.Now)
				if !scanner.ScansEnabled(x.Ctx) {
					return nil
				}
				if x.Namer == nil {
					return errors.New("proposal scans are not bound to a classifier")
				}
				namer, err := x.Namer(x.Ctx)
				if err != nil {
					return err
				}
				out, err := scanner.WithNamer(namer).ManualScans(x.Ctx, x.Now())
				if err != nil {
					return err
				}
				var b strings.Builder
				b.WriteByte('{')
				for i, s := range mailapp.ScanKinds {
					if i > 0 {
						b.WriteByte(',')
					}
					key, _ := json.Marshal(s.Key)
					value, _ := json.Marshal(out[s.Key])
					b.Write(key)
					b.WriteByte(':')
					b.Write(value)
				}
				b.WriteByte('}')
				x.JSON(json.RawMessage(b.String()))
				return nil
			}
		}),
		o.leaf("list", func(a *cli.Args) body {
			status := a.String("status", "", cli.Opt{Choices: proposalsapp.Statuses[:]})
			kind := a.String("kind", "", cli.Opt{})
			return func(x *exec) error {
				found, err := x.Reads.Proposals(x.Ctx, reviewapp.ProposalsInput{Status: *status, Kind: *kind})
				if err != nil {
					return err
				}
				x.JSON(found.Data)
				return nil
			}
		}),
		o.leaf("approve", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			selection := a.String("selection", "", cli.Opt{Help: "JSON selection document"})
			return func(x *exec) error {
				in := reviewapp.ApproveInput{ProposalID: int64(*id)}
				if *selection != "" {
					chosen, err := selectionOf(*selection)
					if err != nil {
						return err
					}
					in.Selection = chosen
				}
				approved, err := x.Review().Approve(x.Ctx, in)
				if approved.Data.Proposal != nil {
					x.JSON(approved.Data.Proposal.Dict())
				}
				if err != nil {
					return recorded(err, fmt.Sprintf("unknown proposal %d", *id))
				}
				if !approved.Data.Applied {
					return failure(cli.ExitError, errors.New("the apply failed; see the result"))
				}
				return nil
			}
		}),
		o.leaf("dismiss", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			forever := a.Bool("forever", cli.Opt{})
			return func(x *exec) error {
				if _, err := x.Review().Dismiss(x.Ctx, reviewapp.DismissInput{ProposalID: int64(*id), Suppress: *forever}); err != nil {
					return recorded(err, fmt.Sprintf("unknown proposal %d", *id))
				}
				x.JSON(dismissView{ID: int64(*id), Dismissed: true})
				return nil
			}
		}),
		o.leaf("resolve", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			outcome := a.String("outcome", "", cli.Opt{Choices: proposalsapp.Outcomes, Help: "what the operator found: applied, not-applied or partial for a write; undone or not-undone for an undo"})
			return func(x *exec) error {
				resolved, err := x.Review().Resolve(x.Ctx, reviewapp.ResolveInput{Target: reviewapp.Target{Kind: reviewapp.ProposalTarget, ID: int64(*id)}, Outcome: *outcome})
				if err != nil {
					return recorded(err, fmt.Sprintf("unknown proposal %d", *id))
				}
				settled := make([]any, len(resolved.Data.Proposal.Settled))
				for i, n := range resolved.Data.Proposal.Settled {
					settled[i] = n
				}
				x.JSON(map[string]any{"id": resolved.Data.Proposal.ID, "settled": settled})
				return nil
			}
		}),
		o.leaf("undo", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			return func(x *exec) error {
				result, err := x.Review().Undo(x.Ctx, reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.ProposalTarget, ID: int64(*id)}})
				outcome := result.Data.Outcome
				if err == nil || len(outcome.Outcomes) > 0 || outcome.SyncError != nil {
					x.JSON(outcome.Dict())
				}
				return recorded(err, fmt.Sprintf("proposal %d is not applied", *id))
			}
		}),
	}}
}

func (o Options) execute(env *cli.Env, run body) int {
	err := o.run(env, run)
	var exit *exitError
	switch {
	case err == nil:
		return cli.ExitOK
	case errors.As(err, &exit):
		_, _ = fmt.Fprintf(env.Stderr, "error: %s\n", exit.text)
		return exit.code
	}
	return cli.Fail(env, err)
}

func (o Options) run(env *cli.Env, run body) error {
	return o.Session(env.Ctx, func(rt Runtime) error {
		return run(&exec{Runtime: rt, Ctx: env.Ctx, Stdout: env.Stdout, Stderr: env.Stderr, opts: o})
	})
}
