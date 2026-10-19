package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	DefaultHost     = "127.0.0.1"
	shutdownTimeout = 10 * time.Second
)

// Page is one page set: its routes and the template files they render (nil when it brings none).
type Page struct {
	Module    Module
	Templates fs.FS
}

// ServeCommand is `atrium serve`; pages are the page sets the server
// registers, in route order.
func ServeCommand(pages ...Page) *cli.Command {
	return &cli.Command{
		Name: "serve",
		Setup: func(args *cli.Args) cli.Handler {
			host := args.String("host", DefaultHost, cli.Opt{})
			port := args.Int("port", 0, cli.Opt{Required: true})
			mode := args.String("auth", AuthProxy, cli.Opt{Choices: AuthModes[:]})
			hosts := args.Repeated("allowed-host", cli.Opt{Help: "accepted Host header; repeatable (proxy mode requires one, or ATRIUM_PUBLIC_HOST)"})
			return func(env *cli.Env) int {
				return serve(env, serveArgs{host: *host, port: *port, auth: *mode, hosts: *hosts, pages: pages, env: config.OS})
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
	if a.auth == AuthProxy && len(hosts) == 0 {
		_, _ = fmt.Fprintln(env.Stderr, "proxy mode needs --allowed-host or ATRIUM_PUBLIC_HOST")
		return cli.ExitError
	}
	cfg, err := config.Load(a.env)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
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
	if err := db.Migrate(ctx, d, cfg.DBPath); err != nil {
		return err
	}
	if _, err := (&drafts.Store{DB: d, Cfg: cfg}).RecoverStuck(ctx); err != nil {
		return err
	}
	var modules []Module
	var templates []fs.FS
	for _, p := range a.pages {
		modules = append(modules, p.Module)
		if p.Templates != nil {
			templates = append(templates, p.Templates)
		}
	}
	render, err := NewRenderer(cfg, d, tpl.Config{Modules: templates})
	if err != nil {
		return err
	}
	srv, err := New(Options{Cfg: cfg, Env: a.env, DB: d, Auth: a.auth, AllowedHosts: hosts, Render: render, Modules: modules})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(a.host, strconv.Itoa(a.port)))
	if err != nil {
		return err
	}
	return listenAndShutdown(ctx, listener, srv)
}

func listenAndShutdown(ctx context.Context, listener net.Listener, handler http.Handler) error {
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second}
	failed := make(chan error, 1)
	go func() { failed <- httpServer.Serve(listener) }()
	log.Info("listening", "addr", listener.Addr().String())
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	stop, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := httpServer.Shutdown(stop)
	if serveErr := <-failed; !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, serveErr)
	}
	return err
}
