package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/sdnotify"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	defaultHost     = "127.0.0.1"
	shutdownTimeout = 10 * time.Second
)

type NavItem = tpl.NavItem

type Page struct {
	Module    Module
	Templates fs.FS
	Globals   map[string]any
	Public    func(config.Config, config.Env, *db.DB, *settings.Registry) func(*http.Request) bool
	Home      string
	Nav       []tpl.NavItem
	OIDC      oidc.Source
}

type Resource struct {
	Name  string
	Mount Module
}

type Boot interface {
	Open(context.Context) (context.Context, *db.DB, config.Config, func() error, error)
	Start(ctx context.Context, d *Deps, services bool) error
	Alive() bool
}

func ServeCommand(boot Boot, pages ...Page) *cli.Command {
	return &cli.Command{
		Name: "serve",
		Setup: func(args *cli.Args) cli.Handler {
			host := args.String("host", defaultHost, cli.Opt{})
			port := args.Int("port", 0, cli.Opt{Required: true})
			modes := args.Repeated("auth", cli.Opt{Choices: authModes[:], Help: "authentication mode; repeatable (proxy and oidc combine; default proxy)"})
			hosts := args.Repeated("allowed-host", cli.Opt{Help: "accepted Host header; repeatable (proxy mode requires one, or ATRIUM_PUBLIC_HOST)"})
			trusted := args.Repeated("trusted-proxy", cli.Opt{Help: "socket peer CIDR allowed to set CF-Connecting-IP; repeatable"})
			noServices := args.Bool("no-services", cli.Opt{Help: "serve the web UI only; another serve process runs the scheduler and background services"})
			return func(env *cli.Env) int {
				return serve(env, serveArgs{host: *host, port: *port, auth: *modes, hosts: *hosts, trusted: *trusted, pages: pages, env: env.Env, boot: boot, services: !*noServices})
			}
		},
	}
}

type serveArgs struct {
	host     string
	port     int
	auth     []string
	hosts    []string
	trusted  []string
	pages    []Page
	env      config.Env
	settings *settings.Registry
	state    *config.State
	boot     Boot
	services bool
}

func publicHosts(given []string, env config.Env) []string {
	if len(given) > 0 {
		return given
	}
	if host, _ := env("ATRIUM_PUBLIC_HOST"); strings.TrimSpace(host) != "" {
		return []string{strings.TrimSpace(host)}
	}
	return nil
}

func trustedProxies(hostValue string, flags []string) ([]netip.Prefix, error) {
	var values []string
	for _, part := range strings.Split(hostValue, ",") {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	for _, flag := range flags {
		if flag = strings.TrimSpace(flag); flag != "" {
			values = append(values, flag)
		}
	}
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			addr, addrErr := netip.ParseAddr(value)
			if addrErr != nil || addr.Zone() != "" {
				return nil, fmt.Errorf("trusted proxy %q is not an address or CIDR", value)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, fmt.Errorf("trusted proxy %q maps more than the IPv4 space", value)
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		out = append(out, prefix.Masked())
	}
	return out, nil
}

func serve(env *cli.Env, a serveArgs) int {
	a.settings, a.state = settings.From(env.Ctx), config.From(env.Ctx)
	hosts := publicHosts(a.hosts, a.env)
	a, policy, ok := resolveAuth(env.Stderr, a, hosts)
	if !ok {
		return cli.ExitError
	}
	if err := run(env.Ctx, a, hosts, policy); err != nil {
		if env.Ctx.Err() != nil {
			return cli.ExitOK
		}
		log.Error("serve failed", logx.Err, err.Error())
		return cli.ExitError
	}
	return cli.ExitOK
}

func run(ctx context.Context, a serveArgs, hosts []string, policy Policy) error {
	ctx, d, cfg, closeApp, err := a.boot.Open(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := closeApp(); err != nil {
			log.Warn("could not drain the application before closing the database", logx.Err, err.Error())
		}
	}()
	if _, err := a.state.Location(a.env); err != nil {
		return fmt.Errorf("the tz setting is not a time zone: %w", err)
	}
	trusted, err := trustedProxies(a.state.Host().Value("trusted_proxy_cidrs"), a.trusted)
	if err != nil {
		return err
	}
	var modules []Module
	var templates []fs.FS
	globals := map[string]any{}
	var public []func(*http.Request) bool
	var nav []tpl.NavItem
	home := ""
	var oidcSource oidc.Source
	for _, p := range a.pages {
		if p.Module != nil {
			modules = append(modules, p.Module)
		}
		if p.Public != nil {
			public = append(public, p.Public(cfg, a.env, d, a.settings))
		}
		if p.Templates != nil {
			templates = append(templates, p.Templates)
		}
		if p.Home != "" {
			home = p.Home
		}
		if p.OIDC != nil {
			oidcSource = p.OIDC
		}
		maps.Copy(globals, p.Globals)
		nav = append(nav, p.Nav...)
	}
	var sessions *oidc.Service
	if policy.OIDC() {
		sessions = oidc.New(d, a.env, oidc.WithConnections(oidcSource))
		if err := sessions.Check(ctx); err != nil {
			log.Warn("oidc sign-in is not set up; nobody can sign in until it is", logx.Err, err.Error())
		}
	}
	render, err := NewRenderer(tpl.Config{Modules: templates, Globals: globals, State: a.state}, nav...)
	if err != nil {
		return err
	}
	srv, err := New(Options{Ctx: ctx, Cfg: cfg, Env: a.env, DB: d, Settings: a.settings, State: a.state, Auth: a.auth, Policy: &policy, AllowedHosts: hosts, Trusted: trusted, Public: anyPublic(public), Render: render, Modules: modules, OIDC: sessions, Home: home, OnReady: func(deps *Deps) error {
		return a.boot.Start(ctx, deps, a.services)
	}})
	if err != nil {
		return err
	}
	defer srv.Close()
	srv.Hold()
	listener, err := net.Listen("tcp", net.JoinHostPort(a.host, strconv.Itoa(a.port)))
	if err != nil {
		return err
	}
	if path, _ := a.env("ATRIUM_SERVE_ADDR_FILE"); path != "" {
		if err := os.WriteFile(path, []byte(listener.Addr().String()), 0o600); err != nil {
			_ = listener.Close()
			return err
		}
	}
	return listenAndShutdown(ctx, listener, srv, a.env, a.boot.Alive)
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

func listenAndShutdown(ctx context.Context, listener net.Listener, srv *Server, env config.Env, alive func() bool) error {
	httpServer := &http.Server{Handler: srv, ReadHeaderTimeout: 30 * time.Second}
	httpServer.RegisterOnShutdown(srv.StopStreams)
	failed := make(chan error, 1)
	go func() { failed <- httpServer.Serve(listener) }()
	log.Info("listening", "addr", listener.Addr().String())
	notifier := sdnotify.New(func(name string) string { v, _ := env(name); return v })
	srv.Release()
	notifier.Ready()
	notifier.Status("serving " + listener.Addr().String())
	go notifier.RunWatchdog(ctx, alive)
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
