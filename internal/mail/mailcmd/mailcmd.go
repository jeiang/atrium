// Package mailcmd is the mail side of cli.py's commands that are not part of pass orchestration: rules,
// shadow-report, reindex, the proposal scans, live and proposals. Each connects to the database named by the
// environment (migrating it), runs one operation and prints its result the way cli.py does.
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
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/run"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/handlers"
	"github.com/jeiang/atrium/internal/py"
)

// Clients opens an account's mail client for a configuration, as imapclient.Opener does.
type Clients = run.Clients

// Options are what a command reaches outside the database: the mail clients and the clock in seconds (the wall
// clock when nil).
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

// exitError is a failure cli.py reports itself: "error: <text>" on stderr and its own exit status.
type exitError struct {
	code int
	text string
}

func (e *exitError) Error() string { return e.text }

func failure(code int, err error) error { return &exitError{code: code, text: err.Error()} }

// Exec is one command run: the configuration and database the command connected to and its streams.
type Exec struct {
	Ctx    context.Context
	Cfg    config.Config
	DB     *db.DB
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	opts Options
}

// Now is the command's clock in seconds.
func (x *Exec) Now() float64 { return x.opts.now() }

// Print writes text to stdout.
func (x *Exec) Print(text string) { _, _ = io.WriteString(x.Stdout, text) }

// JSON is json.dump(value, sys.stdout, indent=2, default=str) and a newline.
func (x *Exec) JSON(value any) {
	x.Print(py.JSONDumps(value, py.JSONOpts{Indent: 2}) + "\n")
}

// Open is the mail client opener of the configuration.
func (x *Exec) Open() mailops.Opener {
	return mailops.Opener(x.opts.Clients(x.Cfg, config.OS))
}

// Syncer is the post-write syncer of the database.
func (x *Exec) Syncer() *mailsync.Syncer {
	return &mailsync.Syncer{DB: x.DB, Cfg: x.Cfg, Now: x.opts.Now}
}

// Registry is handlers.register: every proposal executor and undoer over the database and its mail clients.
func (x *Exec) Registry() *proposals.Registry {
	registry := proposals.NewRegistry()
	handlers.Register(registry, handlers.Deps{DB: x.DB, Open: x.Open(), Syncer: x.Syncer(), Now: x.opts.Now})
	return registry
}

// Live is live.py over the database with the registered undoers.
func (x *Exec) Live() *live.Live {
	return &live.Live{DB: x.DB, Cfg: x.Cfg, Registry: x.Registry(), Now: x.opts.Now}
}

// Exclusive runs fn holding the database's exclusive lock, as `with db.exclusive(cfg.db_path)`.
func (x *Exec) Exclusive(fn func() error) error {
	lock, err := db.Exclusive(x.Cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	return fn()
}

type body func(x *Exec) error

// leaf is a command that loads the configuration, connects to the migrated database and runs body.
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

// openError is the OSError Python's open() raises for a file it cannot read.
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
