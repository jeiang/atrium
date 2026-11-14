package mailcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/jeiang/atrium/internal/cli"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
)

type Options struct {
	Load        func() (mailapp.Config, error)
	Clients     func(mailapp.Config) mailapp.OpenFunc
	Now         func() float64
	Open        func(context.Context) (*mailapp.Pass, func(), error)
	Standalone  func(mailapp.Config, func() float64) *mailapp.Service
	Application func(*mailapp.Pass) *mailapp.Service
}

func (o Options) now() float64 {
	if o.Now != nil {
		return o.Now()
	}
	return mailapp.WallClock()
}

type exitError struct {
	code int
	text string
}

func (e *exitError) Error() string { return e.text }

func failure(code int, err error) error { return &exitError{code: code, text: err.Error()} }

type exec struct {
	*mailapp.Pass
	Ctx    context.Context
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	opts Options
}

func (x *exec) Now() float64 { return x.opts.now() }

func (x *exec) Print(text string) { _, _ = io.WriteString(x.Stdout, text) }

func (x *exec) JSON(value any) {
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		x.Print(err.Error() + "\n")
		return
	}
	x.Print(string(out) + "\n")
}

func (x *exec) Exclusive(fn func() error) error {
	lock, err := mailapp.Exclusive(x.Ctx, x.Cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	return fn()
}

func (x *exec) PassLocked(fn func() error) error {
	held, locks, err := mailapp.PassLocks(x.Ctx, x.Cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = locks.Close() }()
	outer := x.Ctx
	x.Ctx = held
	defer func() { x.Ctx = outer }()
	return fn()
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

func group(name string, leaves ...*cli.Command) *cli.Command {
	return &cli.Command{Name: name, Commands: leaves}
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
	pass, release, err := o.Open(env.Ctx)
	if err != nil {
		return err
	}
	defer release()
	return run(&exec{Pass: pass, Ctx: env.Ctx, Stdin: env.Stdin, Stdout: env.Stdout, Stderr: env.Stderr, opts: o})
}

func openError(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s: no such file or directory", path)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%s: permission denied", path)
	case errors.Is(err, syscall.EISDIR):
		return fmt.Errorf("%s: is a directory", path)
	}
	return err
}

func (o Options) SyncCommand() *cli.Command {
	return &cli.Command{Name: "sync", Setup: func(a *cli.Args) cli.Handler {
		account := a.String("account", "", cli.Opt{})
		return func(env *cli.Env) int {
			cfg, err := o.Load()
			if err == nil {
				_, err = o.Standalone(cfg, o.now).WithOpen(o.Clients(cfg)).Sync(env.Ctx, mailapp.SyncInput{Account: *account})
			}
			if code, ok := unknownAccount(env, *account, err); ok {
				return code
			}
			if err != nil {
				return cli.Fail(env, err)
			}
			return cli.ExitOK
		}
	}}
}

func unknownAccount(env *cli.Env, requested string, err error) (int, bool) {
	var unknown mailapp.UnknownAccount
	if !errors.As(err, &unknown) {
		return 0, false
	}
	var names []string
	for _, name := range mailapp.AccountNames(env.Ctx) {
		names = append(names, strconv.Quote(name))
	}
	slices.Sort(names)
	return env.UsageError("argument --account: invalid choice %q (choose from %s)", requested, strings.Join(names, ", ")), true
}
