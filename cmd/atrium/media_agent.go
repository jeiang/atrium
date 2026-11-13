package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mediaagent"
	"github.com/jeiang/atrium/internal/sdnotify"
)

const mediaAgentCommand = "media-agent"

func runMediaAgent(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	c := cli.New("atrium", nil)
	c.Add(&cli.Command{
		Name: mediaAgentCommand,
		Help: "serve the rooted media filesystem to the main atrium process",
		Setup: func(a *cli.Args) cli.Handler {
			path := a.String("config", "", cli.Opt{Required: true, Metavar: "FILE", Help: "media agent TOML configuration file"})
			return func(env *cli.Env) int { return serveMediaAgent(env.Ctx, *path, env.Stdout, env.Stderr) }
		},
	})
	return c.Run(ctx, args, stdout, stderr)
}

func serveMediaAgent(ctx context.Context, path string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return cli.ExitError
	}
	cfg, err := mediaagent.LoadConfig(path)
	if err != nil {
		return fail(err)
	}
	agent, err := mediaagent.New(cfg)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = agent.Close() }()
	for _, key := range agent.Quarantined() {
		_, _ = fmt.Fprintf(stderr, "warning: receipt %s is unreadable and was quarantined\n", key)
	}
	ln, err := agent.Listen()
	if err != nil {
		return fail(err)
	}
	vpn, err := agent.ListenVPN()
	if err != nil {
		_ = ln.Close()
		return fail(err)
	}
	n := sdnotify.New(os.Getenv)
	stopWatch := n.Watch(ctx, nil)
	defer stopWatch()
	_, _ = fmt.Fprintf(stdout, "media agent listening on %s\n", ln.Addr())
	if vpn != nil {
		_, _ = fmt.Fprintf(stdout, "media agent VPN listening on %s\n", vpn.Addr())
	}
	n.Ready()
	if err := agent.Serve(ctx, ln, vpn, n.Stopping); err != nil {
		return fail(err)
	}
	if sig, _ := cli.Received(ctx); sig == syscall.SIGHUP {
		return cli.ExitHangup
	}
	return cli.ExitOK
}
