package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/sdnotify"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

type ListenOptions struct {
	Plan  web.Plan
	Pages []web.Page
	Tools registry.Source
	Now   func() float64
}

func (b *Bindings) ServeCommand(pages ...web.Page) *cli.Command {
	return &cli.Command{
		Name: "serve",
		Setup: func(args *cli.Args) cli.Handler {
			flags := web.DefineServeFlags(args)
			return func(env *cli.Env) int {
				plan, ok := flags().Resolve(env.Stderr, env.Env)
				if !ok {
					return cli.ExitError
				}
				if err := b.serve(env.Ctx, ListenOptions{Plan: plan, Pages: pages, Tools: b.tools, Now: b.now}); err != nil {
					if env.Ctx.Err() != nil && !errors.As(err, new(*uncleanStop)) {
						return cli.Fail(env, err)
					}
					log.Error("serve did not stop cleanly", logx.Err, err.Error())
					return cli.ExitError
				}
				return cli.ExitOK
			}
		},
	}
}

func (b *Bindings) serve(ctx context.Context, o ListenOptions) error {
	a, ctx, err := b.open(ctx, Resident)
	if err != nil {
		return err
	}
	err = a.Serve(ctx, o)
	if cerr := a.Close(); cerr != nil {
		err = errors.Join(err, &uncleanStop{cerr})
	}
	return err
}

type uncleanStop struct{ err error }

func (e *uncleanStop) Error() string { return e.err.Error() }

func (e *uncleanStop) Unwrap() error { return e.err }

func (a *App) Serve(ctx context.Context, o ListenOptions) error {
	if _, err := a.state.Location(a.env); err != nil {
		return fmt.Errorf("the tz setting is not a time zone: %w", err)
	}
	a.work, a.stopWork = context.WithCancelCause(context.WithoutCancel(ctx))
	ls, err := bind(o.Plan, ops.Address(ctx))
	if err != nil {
		return err
	}
	drain, startDrain := context.WithCancel(ctx)
	defer startDrain()
	public, emergency, err := o.Plan.Build(web.Site{Ctx: a.work, Cfg: a.config, Env: a.env, DB: a.db, Settings: settings.From(ctx), State: a.state, Pages: o.Pages, Drain: a.drainOnStop, Start: func(d *web.Deps) error {
		return a.start(drain, d, o.Tools, o.Now)
	}})
	if err != nil {
		return errors.Join(err, ls.close())
	}
	sites := []*web.Server{public}
	if emergency != nil {
		sites = append(sites, emergency)
	}
	if path, _ := a.env("ATRIUM_SERVE_ADDR_FILE"); path != "" {
		if err := os.WriteFile(path, []byte(ls.public.net.Addr().String()), 0o600); err != nil {
			for _, s := range sites {
				err = errors.Join(err, s.Close())
			}
			return errors.Join(err, ls.close())
		}
	}
	ls.public.srv = public.HTTP()
	if emergency != nil {
		ls.emergency.srv = emergency.HTTP()
	}
	if ls.ops != nil {
		checks := slices.Concat(a.catalog.Checks(), []ops.Check{{Name: "services", Critical: true, Run: func(context.Context, *db.DB) error { return a.ready() }}})
		ls.ops.srv = ops.Server(context.WithoutCancel(ctx), a.db, a.env, a.catalog.Metrics(), checks, a.health)
	}
	return a.run(drain, startDrain, ls, sites)
}

func (a *App) run(ctx context.Context, startDrain context.CancelFunc, ls *listeners, sites []*web.Server) error {
	served := ls.all()
	failed := make(chan error, len(served))
	for _, l := range served {
		go l.serve(failed)
		log.Info("listening", "listener", l.name, "addr", l.net.Addr().String())
	}
	for _, s := range sites {
		s.Release()
	}
	notifier := sdnotify.New(func(name string) string { v, _ := a.env(name); return v })
	notifier.Ready()
	notifier.Status("serving " + ls.public.net.Addr().String())
	a.watchdog = notifier.Watch(context.WithoutCancel(ctx), a.alive)
	var err error
	select {
	case err = <-failed:
	case <-ctx.Done():
	}
	a.draining.Store(true)
	startDrain()
	if a.live != nil {
		a.live.svc.Interrupt()
	}
	sd := newShutdown()
	a.stopping = sd
	notifier.Stopping()
	notifier.Status("draining")
	for _, s := range sites {
		s.StopStreams()
	}
	errs := make(chan error, len(served))
	for _, l := range served {
		if l != ls.ops {
			go func() { errs <- l.shutdown(sd) }()
		}
	}
	for _, l := range served {
		if l != ls.ops {
			err = errors.Join(err, <-errs)
		}
	}
	sd.last = func() error {
		var err error
		for _, s := range sites {
			err = errors.Join(err, s.Close())
		}
		if ls.ops == nil {
			return err
		}
		return errors.Join(err, ls.ops.shutdown(sd))
	}
	if err != nil {
		return &uncleanStop{err}
	}
	return nil
}

func (a *App) start(ctx context.Context, d *web.Deps, tools registry.Source, now func() float64) error {
	set := module.SetFrom(ctx)
	if err := set.Start(ctx, d); err != nil {
		return err
	}
	if err := a.bindLive(ctx, tools, now); err != nil {
		return err
	}
	svc := a.Assistant()
	d.ResumeRun = func(ctx context.Context, runID int64) error {
		err := svc.StartResume(ctx, runID)
		var notInterrupted service.NotInterrupted
		if errors.As(err, &notInterrupted) {
			return nil
		}
		return err
	}
	return a.serve(ctx, set)
}
