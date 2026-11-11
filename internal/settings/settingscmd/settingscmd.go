package settingscmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const actor = "operator"

type Guard func(ctx context.Context, q db.Querier, key, value string) error

type Refused struct{ Err error }

func (r *Refused) Error() string { return r.Err.Error() }

func (r *Refused) Unwrap() error { return r.Err }

func Command(env config.Env, guard Guard) *cli.Command {
	s := &service{env: env, guard: guard}
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
	env   config.Env
	guard Guard
}

func (s *service) list(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		return s.with(e, func(d *db.DB, store *settings.Store) (int, error) {
			snap, err := store.Snapshot(e.Ctx)
			if err != nil {
				return 0, err
			}
			rows := make([]settingRow, 0)
			for _, def := range settings.From(e.Ctx).Defs() {
				row, err := s.row(e.Ctx, store, snap, def.Key)
				if err != nil {
					return 0, err
				}
				rows = append(rows, row)
			}
			writeRows(e.Stdout, rows)
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
		return s.keyed(e, *key, func(d *db.DB, _ *settings.Store) error {
			ctx := audit.With(e.Ctx, actor, audit.CLI)
			return d.WithTx(ctx, func(tx *sql.Tx) error {
				if s.guard != nil {
					if err := s.guard(ctx, tx, *key, *value); err != nil {
						return &Refused{Err: err}
					}
				}
				return settings.From(ctx).Set(ctx, tx, *key, *value)
			})
		})
	}
}

func (s *service) unset(a *cli.Args) cli.Handler {
	key := a.Arg("key", cli.Opt{})
	return func(e *cli.Env) int {
		return s.keyed(e, *key, func(d *db.DB, _ *settings.Store) error {
			ctx := audit.With(e.Ctx, actor, audit.CLI)
			return d.WithTx(ctx, func(tx *sql.Tx) error { return settings.From(ctx).Unset(ctx, tx, *key) })
		})
	}
}

func (s *service) keyed(e *cli.Env, key string, act func(*db.DB, *settings.Store) error) int {
	def, ok := settings.From(e.Ctx).Lookup(key)
	if !ok {
		if host, hosted := settings.HostDefOf(key); hosted {
			if act != nil {
				_, _ = fmt.Fprintf(e.Stderr, "error: %s is host configuration and is not editable here; set %s.%s in the host file\n", key, host.Module, host.Key)
				return cli.ExitError
			}
			return s.with(e, func(*db.DB, *settings.Store) (int, error) {
				writeRows(e.Stdout, settingRow{Key: key, Value: config.From(e.Ctx).HostValue(key), Source: "host", Class: "host"})
				return cli.ExitOK, nil
			})
		}
		if entry, known := settings.ImportEntryFor(key); known {
			_, _ = fmt.Fprintf(e.Stderr, "error: %s is not an operator setting; use %s\n", key, settings.CutoverDestination(entry))
			return cli.ExitError
		}
		return e.UsageError("%v", settings.UnknownKey(key))
	}
	if def.IsSecret() {
		_, _ = fmt.Fprintf(e.Stderr, "error: %s is a secret and is not read or written here\n", key)
		return cli.ExitError
	}
	return s.with(e, func(d *db.DB, store *settings.Store) (int, error) {
		if act != nil {
			if err := act(d, store); err != nil {
				return failure(e, err), nil
			}
		}
		snap, err := store.Snapshot(e.Ctx)
		if err != nil {
			return 0, err
		}
		row, err := s.row(e.Ctx, store, snap, key)
		if err != nil {
			return 0, err
		}
		writeRows(e.Stdout, row)
		return cli.ExitOK, nil
	})
}

func failure(e *cli.Env, err error) int {
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	var notEditable *settings.NotEditable
	var host settings.HostOwned
	var refused *Refused
	if errors.As(err, &invalid) || errors.As(err, &pinned) || errors.As(err, &notEditable) || errors.As(err, &host) || errors.As(err, &refused) {
		_, _ = fmt.Fprintf(e.Stderr, "error: %v\n", err)
		return cli.ExitError
	}
	return cli.Fail(e, err)
}

func (s *service) with(e *cli.Env, run func(*db.DB, *settings.Store) (int, error)) int {
	d, store, err := config.OpenStore(e.Ctx, s.env)
	if err != nil {
		return cli.Fail(e, err)
	}
	defer func() { _ = d.Close() }()
	code, err := run(d, store)
	if err != nil {
		return cli.Fail(e, err)
	}
	return code
}

type settingRow struct {
	Key       string `json:"key"`
	Value     any    `json:"value"`
	Source    any    `json:"source"`
	PinSource string `json:"pin_source,omitempty"`
	Class     string `json:"class"`
}

func writeRows(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (s *service) row(ctx context.Context, store *settings.Store, snap *settings.Snapshot, key string) (settingRow, error) {
	def, _ := settings.From(ctx).Lookup(key)
	if def.IsSecret() {
		return settingRow{Key: key, Value: nil, Source: nil, Class: def.Class()}, nil
	}
	found, err := store.Resolve(ctx, snap, key)
	if err != nil {
		return settingRow{}, err
	}
	return settingRow{Key: key, Value: found.Value, Source: source(found), PinSource: found.PinSource, Class: def.Class()}, nil
}

func source(r settings.Resolved) string {
	if r.Origin == settings.FromInstance {
		return "db"
	}
	return string(r.Origin)
}
