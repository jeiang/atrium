package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/sdnotify"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	DefaultHost     = "127.0.0.1"
	shutdownTimeout = 10 * time.Second
)

var (
	recoverInterval = time.Minute
	recoverClock    = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
)

func recoverProposals(ctx context.Context, d *db.DB) error {
	n, err := proposals.RecoverStuck(ctx, d, recoverClock())
	if n > 0 {
		log.Warn("recovered interrupted approved proposals", "count", n)
	}
	return err
}

func recoverEvery(ctx context.Context, d *db.DB, cfg config.Config) {
	t := time.NewTicker(recoverInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := db.Ready(ctx, d); err != nil && ctx.Err() == nil {
				log.Error("database refresh failed", logx.Err, err.Error())
			}
			if err := recoverProposals(ctx, d); err != nil && ctx.Err() == nil {
				log.Error("proposal recovery failed", logx.Err, err.Error())
			}
			if _, err := (&drafts.Store{DB: d, Cfg: cfg}).RecoverStuck(ctx); err != nil && ctx.Err() == nil {
				log.Error("draft recovery failed", logx.Err, err.Error())
			}
		}
	}
}

type Page struct {
	Module    Module
	Templates fs.FS
	Globals   map[string]any
	Public    func(config.Config, config.Env, *db.DB) func(*http.Request) bool
}

func ServeCommand(pages ...Page) *cli.Command {
	return &cli.Command{
		Name: "serve",
		Setup: func(args *cli.Args) cli.Handler {
			host := args.String("host", DefaultHost, cli.Opt{})
			port := args.Int("port", 0, cli.Opt{Required: true})
			mode := args.String("auth", AuthProxy, cli.Opt{Choices: AuthModes[:]})
			hosts := args.Repeated("allowed-host", cli.Opt{Help: "accepted Host header; repeatable (proxy mode requires one, or ATRIUM_PUBLIC_HOST)"})
			force := args.Bool("force-disable-auth", cli.Opt{Help: "start without authentication when none is configured"})
			return func(env *cli.Env) int {
				return serve(env, serveArgs{host: *host, port: *port, auth: *mode, hosts: *hosts, forceDisableAuth: *force, pages: pages, env: config.OS})
			}
		},
	}
}

type serveArgs struct {
	host  string
	port  int
	auth  string
	hosts []string
	pages []Page
	env   config.Env

	forceDisableAuth bool
	authDisabled     bool
}

func publicHosts(given []string, env config.Env) []string {
	if len(given) > 0 {
		return given
	}
	if host, _ := env("ATRIUM_PUBLIC_HOST"); py.Strip(host) != "" {
		return []string{py.Strip(host)}
	}
	return nil
}

func serve(env *cli.Env, a serveArgs) int {
	hosts := publicHosts(a.hosts, a.env)
	a, ok := resolveAuth(env.Stderr, a, hosts)
	if !ok {
		return cli.ExitError
	}
	cfg, err := config.Load(a.env)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
		return cli.ExitError
	}
	if _, err := config.Location(a.env); err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "error: the tz setting is not a time zone: %s. Use an IANA name such as America/Port_of_Spain.\n", py.Repr(config.TZName(a.env)))
		return cli.ExitError
	}
	if err := run(env.Ctx, cfg, a, hosts); err != nil {
		log.Error("serve failed", logx.Err, err.Error())
		return cli.ExitError
	}
	return cli.ExitOK
}

func run(ctx context.Context, cfg config.Config, a serveArgs, hosts []string) error {
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := config.Migrate(ctx, d, cfg); err != nil {
		return err
	}
	if _, err := config.Location(a.env); err != nil {
		return fmt.Errorf("the tz setting is not a time zone: %w", err)
	}
	if _, err := (&drafts.Store{DB: d, Cfg: cfg}).RecoverStuck(ctx); err != nil {
		return err
	}
	if err := recoverProposals(ctx, d); err != nil {
		return err
	}
	recovering, stopRecovering := context.WithCancel(ctx)
	recovered := make(chan struct{})
	go func() {
		defer close(recovered)
		recoverEvery(recovering, d, cfg)
	}()
	defer func() {
		stopRecovering()
		<-recovered
	}()
	var modules []Module
	var templates []fs.FS
	globals := map[string]any{"auth_disabled": a.authDisabled}
	var public []func(*http.Request) bool
	for _, p := range a.pages {
		modules = append(modules, p.Module)
		if p.Public != nil {
			public = append(public, p.Public(cfg, a.env, d))
		}
		if p.Templates != nil {
			templates = append(templates, p.Templates)
		}
		maps.Copy(globals, p.Globals)
	}
	render, err := NewRenderer(cfg, d, tpl.Config{Modules: templates, Globals: globals})
	if err != nil {
		return err
	}
	srv, err := New(Options{Cfg: cfg, Env: a.env, DB: d, Auth: a.auth, AllowedHosts: hosts, Public: anyPublic(public), Render: render, Modules: modules})
	if err != nil {
		return err
	}
	defer srv.Close()
	srv.Hold()
	listener, err := net.Listen("tcp", net.JoinHostPort(a.host, strconv.Itoa(a.port)))
	if err != nil {
		return err
	}
	return listenAndShutdown(ctx, listener, srv)
}

func anyPublic(checks []func(*http.Request) bool) func(*http.Request) bool {
	if len(checks) == 0 {
		return nil
	}
	return func(r *http.Request) bool {
		for _, check := range checks {
			if check(r) {
				return true
			}
		}
		return false
	}
}

func listenAndShutdown(ctx context.Context, listener net.Listener, srv *Server) error {
	httpServer := &http.Server{Handler: srv, ReadHeaderTimeout: 30 * time.Second}
	httpServer.RegisterOnShutdown(srv.StopStreams)
	failed := make(chan error, 1)
	go func() { failed <- httpServer.Serve(listener) }()
	log.Info("listening", "addr", listener.Addr().String())
	notifier := sdnotify.FromEnv()
	srv.Release()
	notifier.Ready()
	notifier.Status("serving " + listener.Addr().String())
	go notifier.RunWatchdog(ctx, nil)
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	srv.StopStreams()
	notifier.Stopping()
	stop, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := httpServer.Shutdown(stop)
	if serveErr := <-failed; !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, serveErr)
	}
	return err
}
