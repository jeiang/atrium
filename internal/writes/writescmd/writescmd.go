package writescmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
)

type Options struct {
	Env   config.Env
	Now   func() float64
	Store func(*db.DB, config.Config) *ledger.Store
}

func (o Options) now() float64 {
	if o.Now != nil {
		return o.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (o Options) store(d *db.DB, cfg config.Config) *ledger.Store {
	return o.Store(d, cfg)
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
				d, s, err := open(e.Ctx, o)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer func() { _ = d.Close() }()
				if _, err := s.Recover(e.Ctx, o.now()-ledger.StuckSeconds); err != nil {
					return cli.Fail(e, err)
				}
				f := reviewapp.EffectsInput{Origins: reviewapp.DirectOrigins, Operation: *operation, Limit: *limit, BlockedOnly: *blocked}
				if *origin != "" {
					f.Origins = []string{*origin}
				}
				found, err := reviewapp.New(reviewapp.Deps{DB: d, Ledger: s}).Effects(e.Ctx, f)
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
				d, s, err := open(e.Ctx, o)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer func() { _ = d.Close() }()
				if _, err := s.Recover(e.Ctx, o.now()-ledger.StuckSeconds); err != nil {
					return cli.Fail(e, err)
				}
				r := ledger.Resolution{Outcome: *outcome}
				if *evidence != "" {
					r.Evidence = json.RawMessage(*evidence)
				}
				found, err := reviewapp.New(reviewapp.Deps{DB: d, Ledger: s}).Resolve(e.Ctx, reviewapp.ResolveInput{Target: reviewapp.Target{Kind: reviewapp.EffectTarget, ID: int64(*id)}, Outcome: r.Outcome, Evidence: r.Evidence})
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
				d, s, err := open(e.Ctx, o)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer func() { _ = d.Close() }()
				if _, err := s.Recover(e.Ctx, o.now()-ledger.StuckSeconds); err != nil {
					return cli.Fail(e, err)
				}
				found, err := reviewapp.New(reviewapp.Deps{DB: d, Ledger: s}).Undo(e.Ctx, reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.EffectTarget, ID: int64(*id)}})
				if err != nil {
					return cli.Fail(e, err)
				}
				return write(e, found.Data.Undo)
			}
		},
	}
}

func open(ctx context.Context, o Options) (*db.DB, *ledger.Store, error) {
	cfg, err := config.Load(o.Env)
	if err != nil {
		return nil, nil, err
	}
	d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(ctx).DBKey()))
	if err != nil {
		return nil, nil, err
	}
	return d, o.store(d, cfg), nil
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
