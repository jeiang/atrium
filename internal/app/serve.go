package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"time"

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

const shutdownTimeout = 10 * time.Second

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
					if env.Ctx.Err() != nil {
						return cli.ExitOK
					}
					log.Error("serve failed", logx.Err, err.Error())
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
	defer func() {
		if err := a.Close(); err != nil {
			log.Warn("could not drain the application before closing the database", logx.Err, err.Error())
		}
	}()
	return a.Serve(ctx, o)
}

func (a *App) Serve(ctx context.Context, o ListenOptions) error {
	if _, err := a.state.Location(a.env); err != nil {
		return fmt.Errorf("the tz setting is not a time zone: %w", err)
	}
	ls, err := bind(o.Plan, ops.Address(ctx))
	if err != nil {
		return err
	}
	public, emergency, err := o.Plan.Build(web.Site{Ctx: ctx, Cfg: a.config, Env: a.env, DB: a.db, Settings: settings.From(ctx), State: a.state, Pages: o.Pages, Start: func(d *web.Deps) error {
		return a.start(ctx, d, o.Tools, o.Now)
	}})
	if err != nil {
		return errors.Join(err, ls.close())
	}
	sites := []*web.Server{public}
	if emergency != nil {
		sites = append(sites, emergency)
	}
	defer func() {
		for _, s := range sites {
			s.Close()
		}
	}()
	if path, _ := a.env("ATRIUM_SERVE_ADDR_FILE"); path != "" {
		if err := os.WriteFile(path, []byte(ls.public.net.Addr().String()), 0o600); err != nil {
			return errors.Join(err, ls.close())
		}
	}
	ls.public.srv = public.HTTP()
	if emergency != nil {
		ls.emergency.srv = emergency.HTTP()
	}
	if ls.ops != nil {
		checks := slices.Concat(a.catalog.Checks(), []ops.Check{{Name: "services", Critical: true, Run: func(context.Context, *db.DB) error { return a.ready() }}})
		ls.ops.srv = ops.Server(ctx, a.db, a.env, a.catalog.Metrics(), checks)
	}
	return a.run(ctx, ls, sites)
}

func (a *App) run(ctx context.Context, ls *listeners, sites []*web.Server) error {
	served := ls.all()
	results := make(chan error, len(served))
	for _, l := range served {
		go l.serve(results)
		log.Info("listening", "listener", l.name, "addr", l.net.Addr().String())
	}
	for _, s := range sites {
		s.Release()
	}
	notifier := sdnotify.New(func(name string) string { v, _ := a.env(name); return v })
	notifier.Ready()
	notifier.Status("serving " + ls.public.net.Addr().String())
	stopWatchdog := notifier.Watch(ctx, a.alive)
	var err error
	pending := len(served)
	select {
	case err = <-results:
		pending--
	case <-ctx.Done():
	}
	for _, s := range sites {
		s.StopStreams()
	}
	notifier.Stopping()
	stopWatchdog()
	stop, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, l := range served {
		err = errors.Join(err, l.srv.Shutdown(stop))
	}
	for ; pending > 0; pending-- {
		if serveErr := <-results; !errors.Is(serveErr, http.ErrServerClosed) {
			err = errors.Join(err, serveErr)
		}
	}
	return err
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
