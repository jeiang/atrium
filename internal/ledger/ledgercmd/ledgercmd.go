package ledgercmd

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
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
		Name: "effects",
		Help: "inspect, resolve and undo the effect ledger",
		Commands: []*cli.Command{
			list(o),
			show(o),
			resolve(o),
			undo(o),
		},
	}
}

func list(o Options) *cli.Command {
	return &cli.Command{
		Name: "list",
		Help: "list effects, newest first",
		Setup: func(a *cli.Args) cli.Handler {
			origin := a.String("origin", "", cli.Opt{Help: "filter by origin"})
			operation := a.String("operation", "", cli.Opt{Help: "filter by operation"})
			proposal := a.Int("proposal", 0, cli.Opt{Help: "filter by proposal id"})
			thread := a.Int("thread", 0, cli.Opt{Help: "filter by thread id"})
			run := a.Int("run", 0, cli.Opt{Help: "filter by run id"})
			before := a.Int("before", 0, cli.Opt{Help: "only effects older than this id"})
			limit := a.Int("limit", 50, cli.Opt{Help: "maximum effects"})
			blocked := a.Bool("blocked", cli.Opt{Help: "only unresolved effects"})
			return func(e *cli.Env) int {
				d, s, err := open(o)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer func() { _ = d.Close() }()
				if _, err := s.Recover(e.Ctx, o.now()-ledger.StuckSeconds); err != nil {
					return cli.Fail(e, err)
				}
				f := ledger.Filter{Origin: *origin, Operation: *operation, BeforeID: int64(*before), Limit: *limit, BlockedOnly: *blocked}
				if a.Has("proposal") {
					id := int64(*proposal)
					f.ProposalID = &id
				}
				if a.Has("thread") {
					id := int64(*thread)
					f.ThreadID = &id
				}
				if a.Has("run") {
					id := int64(*run)
					f.RunID = &id
				}
				found, err := s.List(e.Ctx, f)
				if err != nil {
					return cli.Fail(e, err)
				}
				return write(e, found)
			}
		},
	}
}

func show(o Options) *cli.Command {
	return &cli.Command{
		Name: "show",
		Help: "show one effect and its latest resolution",
		Setup: func(a *cli.Args) cli.Handler {
			id := a.IntArg("id", cli.Opt{})
			return func(e *cli.Env) int {
				d, s, err := open(o)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer func() { _ = d.Close() }()
				if _, err := s.Recover(e.Ctx, o.now()-ledger.StuckSeconds); err != nil {
					return cli.Fail(e, err)
				}
				found, err := s.Get(e.Ctx, int64(*id))
				if err != nil {
					return cli.Fail(e, err)
				}
				return write(e, found)
			}
		},
	}
}

func resolve(o Options) *cli.Command {
	return &cli.Command{
		Name: "resolve",
		Help: "record what an unknown, partial or expired effect actually did",
		Setup: func(a *cli.Args) cli.Handler {
			id := a.IntArg("id", cli.Opt{})
			outcome := a.String("outcome", "", cli.Opt{Required: true, Choices: []string{"applied", "not-applied", "partial", "undone", "not-undone"},
				Help: "applied, not-applied or partial for a forward effect; undone, not-undone or partial for an undo"})
			evidence := a.String("evidence", "", cli.Opt{Help: "JSON evidence recorded with the resolution"})
			actor := a.String("actor", "operator", cli.Opt{Help: "who resolved it"})
			source := a.String("source", "cli", cli.Opt{Choices: []string{"cli", "ui", "phone"}, Help: "resolution source"})
			return func(e *cli.Env) int {
				d, s, err := open(o)
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
				found, err := s.Resolve(e.Ctx, int64(*id), r, ledger.Resolver{Actor: *actor, Source: *source}, nil)
				if err != nil {
					return cli.Fail(e, err)
				}
				return write(e, found)
			}
		},
	}
}

func undo(o Options) *cli.Command {
	return &cli.Command{
		Name: "undo",
		Help: "undo a completed or partial effect",
		Setup: func(a *cli.Args) cli.Handler {
			id := a.IntArg("id", cli.Opt{})
			return func(e *cli.Env) int {
				d, s, err := open(o)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer func() { _ = d.Close() }()
				if _, err := s.Recover(e.Ctx, o.now()-ledger.StuckSeconds); err != nil {
					return cli.Fail(e, err)
				}
				found, err := s.Undo(e.Ctx, int64(*id), ledger.Resolver{Actor: "operator", Source: "cli"})
				if err != nil {
					return cli.Fail(e, err)
				}
				return write(e, found)
			}
		},
	}
}

func open(o Options) (*db.DB, *ledger.Store, error) {
	cfg, err := config.Load(o.Env)
	if err != nil {
		return nil, nil, err
	}
	d, err := db.Connect(cfg.DBPath)
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
