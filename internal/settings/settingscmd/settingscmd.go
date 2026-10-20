package settingscmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const actor = "operator"

func Command() *cli.Command {
	s := &service{env: config.OS, reg: settings.Default}
	return &cli.Command{
		Name: "settings",
		Commands: []*cli.Command{
			{Name: "list", Help: "list every setting with its effective value, source and class", Setup: s.list},
			{Name: "get", Help: "print one setting", Setup: s.get},
			{Name: "set", Help: "store an instance-level value", Setup: s.set},
			{Name: "unset", Help: "remove the stored instance-level value", Setup: s.unset},
		},
	}
}

type service struct {
	env config.Env
	reg *settings.Registry
}

func (s *service) list(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		return s.with(e, func(d *db.DB) (int, error) {
			rows := make([]any, 0)
			for _, def := range s.reg.Defs() {
				row, err := s.row(e.Ctx, d, def.Key)
				if err != nil {
					return 0, err
				}
				rows = append(rows, row)
			}
			_, _ = fmt.Fprintln(e.Stdout, py.JSONDumps(rows, py.JSONOpts{Indent: 2}))
			return cli.ExitOK, nil
		})
	}
}

func (s *service) get(a *cli.Args) cli.Handler {
	key := a.Arg("key", cli.Opt{})
	return func(e *cli.Env) int {
		return s.keyed(e, *key, nil)
	}
}

func (s *service) set(a *cli.Args) cli.Handler {
	key := a.Arg("key", cli.Opt{})
	value := a.Arg("value", cli.Opt{})
	return func(e *cli.Env) int {
		return s.keyed(e, *key, func(d *db.DB) error {
			ctx := audit.With(e.Ctx, actor, audit.CLI)
			return d.WithTx(ctx, func(tx *sql.Tx) error { return s.reg.Set(ctx, tx, *key, *value, 0) })
		})
	}
}

func (s *service) unset(a *cli.Args) cli.Handler {
	key := a.Arg("key", cli.Opt{})
	return func(e *cli.Env) int {
		return s.keyed(e, *key, func(d *db.DB) error {
			ctx := audit.With(e.Ctx, actor, audit.CLI)
			return d.WithTx(ctx, func(tx *sql.Tx) error { return s.reg.Unset(ctx, tx, *key, 0) })
		})
	}
}

func (s *service) keyed(e *cli.Env, key string, act func(*db.DB) error) int {
	def, ok := s.reg.Lookup(key)
	if !ok {
		return e.UsageError("%v", settings.UnknownKey(key))
	}
	if def.Secret {
		_, _ = fmt.Fprintf(e.Stderr, "error: %s is a secret and is not read or written here\n", key)
		return cli.ExitError
	}
	return s.with(e, func(d *db.DB) (int, error) {
		if act != nil {
			if err := act(d); err != nil {
				return failure(e, err), nil
			}
		}
		row, err := s.row(e.Ctx, d, key)
		if err != nil {
			return 0, err
		}
		_, _ = fmt.Fprintln(e.Stdout, py.JSONDumps(row, py.JSONOpts{Indent: 2}))
		return cli.ExitOK, nil
	})
}

func failure(e *cli.Env, err error) int {
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	var scope *settings.ScopeError
	if errors.As(err, &invalid) || errors.As(err, &pinned) || errors.As(err, &scope) {
		_, _ = fmt.Fprintf(e.Stderr, "error: %v\n", err)
		return cli.ExitError
	}
	return cli.Fail(e, err)
}

func (s *service) with(e *cli.Env, run func(*db.DB) (int, error)) int {
	cfg, err := config.Load(s.env)
	if err != nil {
		return cli.Fail(e, err)
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return cli.Fail(e, err)
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(e.Ctx, d, cfg.DBPath); err != nil {
		return cli.Fail(e, err)
	}
	code, err := run(d)
	if err != nil {
		return cli.Fail(e, err)
	}
	return code
}

func (s *service) row(ctx context.Context, d *db.DB, key string) (py.Dict, error) {
	def, _ := s.reg.Lookup(key)
	if def.Secret {
		return py.Dict{{Key: "key", Value: key}, {Key: "value", Value: nil}, {Key: "source", Value: nil}, {Key: "class", Value: string(def.Class)}}, nil
	}
	found, err := s.reg.Resolve(ctx, d.Read, key, 0)
	if err != nil {
		return nil, err
	}
	row := py.Dict{{Key: "key", Value: key}, {Key: "value", Value: found.Value}, {Key: "source", Value: source(found)}}
	if found.Pinned() {
		row = append(row, py.Pair{Key: "pin_source", Value: found.PinSource})
	}
	return append(row, py.Pair{Key: "class", Value: string(def.Class)}), nil
}

func source(r settings.Resolved) string {
	if r.Origin == settings.FromInstance {
		return "db"
	}
	return string(r.Origin)
}
