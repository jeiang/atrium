package secretscmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const actor = "operator"

const maxValue = 1 << 20

func Command() *cli.Command {
	s := &service{env: config.OS}
	return &cli.Command{
		Name: "secrets",
		Commands: []*cli.Command{
			{Name: "set", Help: "store a secret read from standard input", Setup: s.set},
			{Name: "delete", Help: "remove a stored secret", Setup: s.delete},
			{Name: "status", Help: "show whether each secret is set, unset or pinned by config", Setup: s.status},
		},
	}
}

type service struct{ env config.Env }

func (s *service) set(a *cli.Args) cli.Handler {
	name := a.Arg("name", cli.Opt{})
	a.RejectExtras("the value must come from standard input, not the command line")
	return func(e *cli.Env) int {
		known := s.with(e, func(st *settings.Store) int {
			if err := st.Known(*name); err != nil {
				return failure(e, err)
			}
			return cli.ExitOK
		})
		if known != cli.ExitOK {
			return known
		}
		raw, err := io.ReadAll(io.LimitReader(e.Stdin, maxValue+1))
		if err != nil {
			return cli.Fail(e, err)
		}
		value := strings.TrimRight(string(raw), "\r\n")
		if len(raw) > maxValue {
			return e.UsageError("the secret on standard input is larger than %d bytes", maxValue)
		}
		return s.with(e, func(st *settings.Store) int {
			return write(e, st, *name, func(ctx context.Context, w *settings.Store) error { return w.Set(ctx, *name, value, "cli") })
		})
	}
}

func (s *service) delete(a *cli.Args) cli.Handler {
	name := a.Arg("name", cli.Opt{})
	return func(e *cli.Env) int {
		return s.with(e, func(st *settings.Store) int {
			return write(e, st, *name, func(ctx context.Context, w *settings.Store) error { return w.Delete(ctx, *name) })
		})
	}
}

func (s *service) status(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		return s.with(e, func(st *settings.Store) int {
			for _, def := range settings.Default.Defs() {
				if def.IsSecret() {
					if code := report(e, st, def.Key); code != cli.ExitOK {
						return code
					}
				}
			}
			return cli.ExitOK
		})
	}
}

func write(e *cli.Env, st *settings.Store, name string, act func(context.Context, *settings.Store) error) int {
	ctx := audit.With(e.Ctx, actor, audit.CLI)
	err := st.InTx(ctx, func(w *settings.Store) error { return act(ctx, w) })
	if err != nil {
		return failure(e, err)
	}
	return report(e, st, name)
}

func report(e *cli.Env, st *settings.Store, name string) int {
	status, err := st.Status(e.Ctx, name)
	if err != nil {
		return failure(e, err)
	}
	_, _ = fmt.Fprintf(e.Stdout, "%s: %s\n", name, status)
	return cli.ExitOK
}

func failure(e *cli.Env, err error) int {
	var pinned *settings.PinnedError
	var encrypted settings.EncryptionRequired
	var notSecret settings.NotSecret
	var empty settings.EmptyValue
	var pinOnly settings.PinOnly
	var unknown settings.UnknownKey
	switch {
	case errors.As(err, &unknown), errors.As(err, &notSecret):
		return e.UsageError("%v", err)
	case errors.As(err, &pinned), errors.As(err, &encrypted), errors.As(err, &empty), errors.As(err, &pinOnly):
		_, _ = fmt.Fprintf(e.Stderr, "error: %v\n", err)
		return cli.ExitError
	}
	return cli.Fail(e, err)
}

func (s *service) with(e *cli.Env, run func(*settings.Store) int) int {
	d, store, err := config.OpenStore(e.Ctx, s.env)
	if err != nil {
		return cli.Fail(e, err)
	}
	defer func() { _ = d.Close() }()
	return run(store)
}
