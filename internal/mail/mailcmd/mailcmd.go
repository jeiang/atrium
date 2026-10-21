package mailcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"syscall"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/run"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/handlers"
	"github.com/jeiang/atrium/internal/py"
)

type Clients = run.Clients

type Options struct {
	Clients Clients
	Now     func() float64
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

type Exec struct {
	Ctx    context.Context
	Cfg    config.Config
	DB     *db.DB
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	opts Options
}

func (x *Exec) Now() float64 { return x.opts.now() }

func (x *Exec) Print(text string) { _, _ = io.WriteString(x.Stdout, text) }

func (x *Exec) JSON(value any) {
	x.Print(py.JSONDumps(value, py.JSONOpts{Indent: 2}) + "\n")
}

func (x *Exec) Open() mailops.Opener {
	return mailops.Opener(mail.BindMode(x.opts.Clients(x.Cfg), x.DB.Read))
}

func (x *Exec) Syncer() *mailsync.Syncer {
	return &mailsync.Syncer{DB: x.DB, Cfg: x.Cfg, Now: x.opts.Now}
}

func (x *Exec) Registry() *proposals.Registry {
	registry := proposals.NewRegistry()
	handlers.Register(registry, handlers.Deps{DB: x.DB, Open: x.Open(), Syncer: x.Syncer(), Now: x.opts.Now})
	return registry
}

func (x *Exec) Live() *live.Live {
	return &live.Live{DB: x.DB, Cfg: x.Cfg, Registry: x.Registry(), Now: x.opts.Now}
}

func (x *Exec) Exclusive(fn func() error) error {
	lock, err := db.Exclusive(x.Ctx, x.Cfg.DBPath, db.ShortLockWait)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	return fn()
}

type body func(x *Exec) error

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
	cfg, err := config.Load(config.OS)
	if err != nil {
		return err
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(env.Ctx, d, cfg.DBPath); err != nil {
		return err
	}
	return run(&Exec{Ctx: env.Ctx, Cfg: cfg, DB: d, Stdin: env.Stdin, Stdout: env.Stdout, Stderr: env.Stderr, opts: o})
}

func openError(path string, err error) error {
	quoted := py.Repr(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &py.Exception{Class: "FileNotFoundError", Msg: "[Errno 2] No such file or directory: " + quoted}
	case errors.Is(err, fs.ErrPermission):
		return &py.Exception{Class: "PermissionError", Msg: "[Errno 13] Permission denied: " + quoted}
	case errors.Is(err, syscall.EISDIR):
		return &py.Exception{Class: "IsADirectoryError", Msg: "[Errno 21] Is a directory: " + quoted}
	}
	return err
}
