package mailcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"syscall"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	runpkg "github.com/jeiang/atrium/internal/mail/run"
)

type Options struct {
	Env         config.Env
	Clients     func(config.Config) mail.OpenFunc
	Now         func() float64
	Classifiers runpkg.Classifier
	Application func(*runpkg.Pass) *mailapp.Service
}

func (o Options) env() config.Env {
	if o.Env != nil {
		return o.Env
	}
	return config.OS
}

func (o Options) now() float64 {
	if o.Now != nil {
		return o.Now()
	}
	return mail.WallClock()
}

type exitError struct {
	code int
	text string
}

func (e *exitError) Error() string { return e.text }

func failure(code int, err error) error { return &exitError{code: code, text: err.Error()} }

type exec struct {
	*runpkg.Pass
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
	lock, err := db.Exclusive(x.Ctx, x.Cfg.DBPath, db.ShortLockWait)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
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
	cfg, err := config.Load(o.env())
	if err != nil {
		return err
	}
	clients := o.Clients
	if clients == nil {
		clients = imapclient.Opener
	}
	pass, release, err := runpkg.Open(env.Ctx, o.env(), cfg, clients(cfg), o.Now, o.Classifiers)
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
