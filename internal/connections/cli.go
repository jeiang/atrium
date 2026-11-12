package connections

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func Command(env config.Env) *cli.Command {
	s := &service{env: env}
	return &cli.Command{
		Name: "connections",
		Commands: []*cli.Command{
			{Name: "list", Help: "list connections with their capabilities, credential presence and health", Setup: s.list},
			{Name: "test", Help: "test one connection or every configured connection and save the outcome", Setup: s.test},
		},
	}
}

type service struct{ env config.Env }

func (s *service) open(e *cli.Env) (*db.DB, *Store, int) {
	reg, err := From(e.Ctx)
	if err != nil {
		return nil, nil, cli.Fail(e, err)
	}
	cfg, err := config.Load(s.env)
	if err != nil {
		return nil, nil, cli.Fail(e, err)
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return nil, nil, cli.Fail(e, err)
	}
	if err := config.Boot(e.Ctx, s.env, d, cfg); err != nil {
		_ = d.Close()
		return nil, nil, cli.Fail(e, err)
	}
	store, err := New(d, config.From(e.Ctx).Host(), reg)
	if err != nil {
		_ = d.Close()
		return nil, nil, cli.Fail(e, err)
	}
	return d, store, cli.ExitOK
}

func (s *service) list(a *cli.Args) cli.Handler {
	jsonOut := a.Bool("json", cli.Opt{Help: "print JSON"})
	return func(e *cli.Env) int {
		d, store, code := s.open(e)
		if code != cli.ExitOK {
			return code
		}
		defer func() { _ = d.Close() }()
		views, err := store.List(e.Ctx)
		if err != nil {
			return cli.Fail(e, err)
		}
		if *jsonOut {
			return writeJSON(e, views)
		}
		for _, v := range views {
			_, _ = fmt.Fprintf(e.Stdout, "%s\t%s\t%s\t%s\t%s\n", v.ID, v.Kind, credentialSummary(v), v.State, reasonOr(v.Reason, "-"))
		}
		return cli.ExitOK
	}
}

func (s *service) test(a *cli.Args) cli.Handler {
	id := a.Arg("id", cli.Opt{Help: "connection id"})
	all := a.Bool("all", cli.Opt{Help: "test every configured connection"})
	jsonOut := a.Bool("json", cli.Opt{Help: "print JSON"})
	return func(e *cli.Env) int {
		if (*id == "") == !*all {
			return e.UsageError("give a connection id or --all")
		}
		d, store, code := s.open(e)
		if code != cli.ExitOK {
			return code
		}
		defer func() { _ = d.Close() }()
		var views []View
		if *all {
			list, err := store.List(e.Ctx)
			if err != nil {
				return cli.Fail(e, err)
			}
			for _, v := range list {
				if !v.Enabled || v.State == Unconfigured || v.State == Disabled {
					continue
				}
				tested, err := store.Test(e.Ctx, v.ID)
				if err != nil {
					return cli.Fail(e, err)
				}
				views = append(views, tested)
			}
		} else {
			tested, err := store.Test(e.Ctx, ID(*id))
			if err != nil {
				return cli.Fail(e, err)
			}
			views = append(views, tested)
		}
		if *jsonOut {
			if code := writeJSON(e, views); code != cli.ExitOK {
				return code
			}
		} else {
			for _, v := range views {
				_, _ = fmt.Fprintf(e.Stdout, "%s\t%s\t%s\n", v.ID, v.State, reasonOr(v.Reason, "-"))
			}
		}
		for _, v := range views {
			if v.State == Failing || v.State == NeedsReauth {
				return cli.ExitError
			}
		}
		if !*all && len(views) == 1 && (views[0].State == Unconfigured || views[0].State == Disabled) {
			return cli.ExitError
		}
		return cli.ExitOK
	}
}

func writeJSON(e *cli.Env, v any) int {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return cli.Fail(e, err)
	}
	_, _ = fmt.Fprintln(e.Stdout, string(raw))
	return cli.ExitOK
}

func credentialSummary(v View) string {
	var parts []string
	for _, c := range v.Credentials {
		state := "missing"
		if c.Present {
			state = c.Source
		}
		parts = append(parts, c.Slot+"="+state)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

func reasonOr(reason, fallback string) string {
	if strings.TrimSpace(reason) == "" {
		return fallback
	}
	return reason
}
