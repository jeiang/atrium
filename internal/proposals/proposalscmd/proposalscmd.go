package proposalscmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/mail/scans"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/handlers"
	"github.com/jeiang/atrium/internal/settings"
)

var operator = proposals.Resolver{Actor: "operator", Source: "cli"}

func nullable[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}

type Options struct {
	Clients func(config.Config) mail.OpenFunc
	Now     func() float64
	Env     config.Env
	Enabled func(context.Context, db.Querier, string) (bool, error)
	Store   func(*db.DB, config.Config) *ledger.Store
	Tools   registry.Source
}

func (o Options) store(d *db.DB, cfg config.Config) *ledger.Store {
	if o.Store != nil {
		return o.Store(d, cfg)
	}
	return ledger.NewStore(d, o.now)
}

func (o Options) env() config.Env {
	if o.Env == nil {
		return config.OS
	}
	return o.Env
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
	Ctx    context.Context
	Cfg    config.Config
	DB     *db.DB
	Stdout io.Writer
	Stderr io.Writer
	opts   Options
}

func (x *exec) Now() float64 { return x.opts.now() }

type proposalRow struct {
	ID       int64   `json:"id"`
	Kind     string  `json:"kind"`
	Account  *string `json:"account"`
	Status   string  `json:"status"`
	Title    string  `json:"title"`
	Undoable bool    `json:"undoable"`
}

type dismissView struct {
	ID        int64 `json:"id"`
	Dismissed bool  `json:"dismissed"`
}

func (x *exec) JSON(value any) {
	switch value.(type) {
	case proposals.ProposalView, []proposalRow, dismissView:
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

func (x *exec) registry(store *ledger.Store) *proposals.Registry {
	registry := proposals.NewRegistry()
	clients := x.opts.Clients
	if clients == nil {
		clients = imapclient.Opener
	}
	open := clients(x.Cfg)
	handlers.Register(registry, handlers.Deps{
		DB: x.DB, Ledger: store, Open: open,
		Syncer: &mailsync.Syncer{DB: x.DB, Cfg: x.Cfg, Now: x.opts.Now}, Now: x.opts.Now, Env: x.opts.env(), Registry: x.opts.Tools,
	})
	return registry
}

func (x *exec) Pipeline() *proposals.Pipeline {
	store := x.opts.store(x.DB, x.Cfg)
	return &proposals.Pipeline{
		DB: x.DB, Ledger: store, Registry: x.registry(store), Now: x.opts.Now, Enabled: x.opts.Enabled,
	}
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
				if !mailEnabled(x.Ctx, x.DB.Read) {
					return nil
				}
				url, err := endpoint.ChatURL(x.Ctx, x.DB.Read)
				if err != nil {
					return err
				}
				out, err := scans.ScanAll(x.Ctx, x.DB, x.Now(), rebalance.SuggestName(url))
				if err != nil {
					return err
				}
				var b strings.Builder
				b.WriteByte('{')
				for i, s := range scans.All {
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
			status := a.String("status", "", cli.Opt{Choices: proposals.Statuses[:]})
			kind := a.String("kind", "", cli.Opt{})
			return func(x *exec) error {
				list, err := proposals.List(x.Ctx, x.DB.Read, *status, *kind)
				if err != nil {
					return err
				}
				out := make([]proposalRow, len(list))
				for i, p := range list {
					out[i] = proposalRow{ID: p.ID, Kind: p.Kind, Account: p.Account, Status: p.Status, Title: p.Title, Undoable: p.Undoable}
				}
				x.JSON(out)
				return nil
			}
		}),
		o.leaf("approve", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			selection := a.String("selection", "", cli.Opt{Help: "JSON selection document"})
			return func(x *exec) error {
				var chosen any
				if *selection != "" {
					parsed, err := jsonx.Loads([]byte(*selection))
					if err != nil {
						if strings.TrimSpace(*selection) == "{" {
							return errors.New("Expecting property name enclosed in double quotes: line 1 column 2 (char 1)") //nolint:staticcheck // the CLI contract keeps the py-compatible wording
						}
						return err
					}
					b, err := json.Marshal(parsed)
					if err != nil {
						return err
					}
					if err := json.Unmarshal(b, &chosen); err != nil {
						return err
					}
				}
				approved, err := x.Pipeline().ApproveAndApply(x.Ctx, int64(*id), chosen, operator)
				if err != nil {
					return err
				}
				x.JSON(approved.Dict())
				if approved.Status == proposals.Failed {
					return failure(cli.ExitError, errors.New("the apply failed; see the result"))
				}
				return nil
			}
		}),
		o.leaf("dismiss", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			forever := a.Bool("forever", cli.Opt{})
			return func(x *exec) error {
				status := proposals.Dismissed
				if *forever {
					status = proposals.Suppressed
				}
				if err := x.Pipeline().Dismiss(x.Ctx, int64(*id), status, operator); err != nil {
					return err
				}
				x.JSON(dismissView{ID: int64(*id), Dismissed: true})
				return nil
			}
		}),
		o.leaf("resolve", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			outcome := a.String("outcome", "", cli.Opt{Choices: proposals.Outcomes, Help: "what the operator found: applied, not-applied or partial for a write; undone or not-undone for an undo"})
			return func(x *exec) error {
				resolved, err := x.Pipeline().Resolve(x.Ctx, int64(*id), *outcome, operator)
				var refused proposals.ProposalError
				if errors.As(err, &refused) {
					return failure(cli.ExitError, err)
				}
				if err != nil {
					return err
				}
				settled := make([]any, len(resolved.Settled))
				for i, n := range resolved.Settled {
					settled[i] = n
				}
				x.JSON(map[string]any{"id": resolved.ID, "settled": settled})
				return nil
			}
		}),
		o.leaf("undo", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			return func(x *exec) error {
				result, err := x.Pipeline().Undo(x.Ctx, int64(*id), operator)
				if err != nil {
					return err
				}
				x.JSON(result.Dict())
				return nil
			}
		}),
	}}
}

func mailEnabled(ctx context.Context, q db.Querier) bool {
	flag, err := settings.From(ctx).Resolve(ctx, q, module.FlagKey(module.Mail))
	if err != nil || flag.Value != "true" {
		return false
	}
	found, err := settings.From(ctx).Resolve(ctx, q, endpoint.KeyChatURL)
	if err != nil || found.Value == "" {
		return false
	}
	reason, err := endpoint.EmbeddingConfigured(ctx, q)
	return err == nil && reason == ""
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
	cfg, err := config.Load(o.env())
	if err != nil {
		return err
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := config.Boot(env.Ctx, o.env(), d, cfg); err != nil {
		return err
	}
	if set := module.SetFrom(env.Ctx); set != nil {
		if err := set.Refresh(env.Ctx, d.Read); err != nil {
			return err
		}
	}
	return run(&exec{Ctx: env.Ctx, Cfg: cfg, DB: d, Stdout: env.Stdout, Stderr: env.Stderr, opts: o})
}
