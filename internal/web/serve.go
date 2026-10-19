package web

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/py"
)

const (
	DefaultHost     = "127.0.0.1"
	shutdownTimeout = 10 * time.Second
)

type authFlag string

func (a *authFlag) String() string { return string(*a) }

func (a *authFlag) Set(v string) error {
	for _, mode := range AuthModes {
		if v == mode {
			*a = authFlag(v)
			return nil
		}
	}
	return fmt.Errorf("argument --auth: invalid choice: %s (choose from %s)", py.Repr(v), strings.Join(AuthModes[:], ", "))
}

type hostsFlag []string

func (h *hostsFlag) String() string { return strings.Join(*h, ",") }

func (h *hostsFlag) Set(v string) error {
	*h = append(*h, v)
	return nil
}

// ServeCommand is `atrium serve`; modules are the page sets the server
// registers, in route order.
func ServeCommand(modules ...Module) *cli.Command {
	return &cli.Command{
		Name: "serve",
		Setup: func(fs *flag.FlagSet) cli.Handler {
			host := fs.String("host", DefaultHost, "listen address")
			port := fs.Int("port", 0, "listen port (required)")
			mode := authFlag(AuthProxy)
			fs.Var(&mode, "auth", "proxy or local")
			var hosts hostsFlag
			fs.Var(&hosts, "allowed-host", "accepted Host header; repeatable (proxy mode requires one, or ATRIUM_PUBLIC_HOST)")
			return func(env *cli.Env) int {
				given := false
				fs.Visit(func(f *flag.Flag) { given = given || f.Name == "port" })
				if !given {
					return env.UsageError("the following arguments are required: --port")
				}
				return serve(env, serveArgs{host: *host, port: *port, auth: string(mode), hosts: hosts, modules: modules, env: config.OS})
			}
		},
	}
}

type serveArgs struct {
	host    string
	port    int
	auth    string
	hosts   []string
	modules []Module
	env     config.Env
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
	srv, err := New(Options{Cfg: cfg, Env: a.env, DB: d, Auth: a.auth, AllowedHosts: hosts, Modules: a.modules})
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
