package web

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	defaultHost       = "127.0.0.1"
	readHeaderTimeout = 30 * time.Second
	noEmergency       = -1
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

type ServeFlags struct {
	Host          string
	Port          int
	Auth          []string
	Hosts         []string
	Trusted       []string
	EmergencyHost string
	EmergencyPort int
}

func DefineServeFlags(args *cli.Args) func() ServeFlags {
	host := args.String("host", defaultHost, cli.Opt{})
	port := args.Int("port", 0, cli.Opt{Required: true})
	modes := args.Repeated("auth", cli.Opt{Choices: authModes[:], Help: "authentication mode; repeatable (proxy and oidc combine; default proxy)"})
	hosts := args.Repeated("allowed-host", cli.Opt{Help: "accepted Host header; repeatable (proxy mode requires one, or ATRIUM_PUBLIC_HOST)"})
	trusted := args.Repeated("trusted-proxy", cli.Opt{Help: "socket peer CIDR allowed to set CF-Connecting-IP; repeatable"})
	emergencyHost := args.String("emergency-host", defaultHost, cli.Opt{Help: "numeric loopback address (127.0.0.0/8 or ::1) of the emergency listener"})
	emergencyPort := args.Int("emergency-port", noEmergency, cli.Opt{Help: "port of the emergency listener, which trusts every loopback request like --auth local; omitted runs none"})
	return func() ServeFlags {
		return ServeFlags{Host: *host, Port: *port, Auth: *modes, Hosts: *hosts, Trusted: *trusted, EmergencyHost: *emergencyHost, EmergencyPort: *emergencyPort}
	}
}

type Endpoint struct {
	Addr   string
	policy Policy
	hosts  []string
}

type Plan struct {
	Public    Endpoint
	Emergency *Endpoint
	trusted   []string
}

func (f ServeFlags) Resolve(stderr io.Writer, env config.Env) (Plan, bool) {
	hosts := publicHosts(f.Hosts, env)
	policy, ok := resolveAuth(stderr, f.Host, f.Auth, hosts)
	if !ok {
		return Plan{}, false
	}
	p := Plan{Public: Endpoint{Addr: net.JoinHostPort(f.Host, strconv.Itoa(f.Port)), policy: policy, hosts: hosts}, trusted: f.Trusted}
	if f.EmergencyPort == noEmergency {
		return p, true
	}
	if f.EmergencyPort < 0 || f.EmergencyPort > 65535 {
		_, _ = fmt.Fprintf(stderr, "error: --emergency-port %d is not a port\n", f.EmergencyPort)
		return Plan{}, false
	}
	if !emergencyBind(f.EmergencyHost) {
		_, _ = fmt.Fprintf(stderr, "error: the emergency listener needs a numeric loopback --emergency-host in 127.0.0.0/8 or ::1; %q is not one\n", f.EmergencyHost)
		return Plan{}, false
	}
	p.Emergency = &Endpoint{Addr: net.JoinHostPort(strings.Trim(f.EmergencyHost, "[]"), strconv.Itoa(f.EmergencyPort)), policy: Policy{Mode: ModeLocal}}
	return p, true
}

func emergencyBind(host string) bool {
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && addr.Zone() == "" && (addr.Is4() && addr.IsLoopback() || addr == netip.IPv6Loopback())
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

type Site struct {
	Ctx      context.Context
	Cfg      config.Config
	Env      config.Env
	DB       *db.DB
	Settings *settings.Registry
	State    *config.State
	Pages    []Page
	Start    func(*Deps) error
}

func (p Plan) Build(site Site) (public, emergency *Server, err error) {
	trusted, err := trustedProxies(site.State.Host().Value("trusted_proxy_cidrs"), p.trusted)
	if err != nil {
		return nil, nil, err
	}
	var modules []Module
	var templates []fs.FS
	globals := map[string]any{}
	var checks []func(*http.Request) bool
	var nav []tpl.NavItem
	home := ""
	var oidcSource oidc.Source
	for _, page := range site.Pages {
		if page.Module != nil {
			modules = append(modules, page.Module)
		}
		if page.Public != nil {
			checks = append(checks, page.Public(site.Cfg, site.Env, site.DB, site.Settings))
		}
		if page.Templates != nil {
			templates = append(templates, page.Templates)
		}
		if page.Home != "" {
			home = page.Home
		}
		if page.OIDC != nil {
			oidcSource = page.OIDC
		}
		maps.Copy(globals, page.Globals)
		nav = append(nav, page.Nav...)
	}
	build := func(e Endpoint, sessions *oidc.Service, start func(*Deps) error) (*Server, error) {
		render, err := NewRenderer(tpl.Config{Modules: templates, Globals: globals, State: site.State}, nav...)
		if err != nil {
			return nil, err
		}
		srv, err := New(Options{Ctx: site.Ctx, Cfg: site.Cfg, Env: site.Env, DB: site.DB, Settings: site.Settings, State: site.State, Policy: &e.policy, AllowedHosts: e.hosts, Trusted: trusted, Public: anyPublic(checks), Render: render, Modules: modules, OIDC: sessions, Home: home, OnReady: start})
		if err != nil {
			return nil, err
		}
		srv.Hold()
		return srv, nil
	}
	var sessions *oidc.Service
	if p.Public.policy.OIDC() {
		sessions = oidc.New(site.DB, site.Env, oidc.WithConnections(oidcSource))
		if err := sessions.Check(site.Ctx); err != nil {
			log.Warn("oidc sign-in is not set up; nobody can sign in until it is", logx.Err, err.Error())
		}
	}
	if public, err = build(p.Public, sessions, site.Start); err != nil {
		return nil, nil, err
	}
	if p.Emergency == nil {
		return public, nil, nil
	}
	if emergency, err = build(*p.Emergency, nil, nil); err != nil {
		public.Close()
		return nil, nil, err
	}
	return public, emergency, nil
}

func (s *Server) HTTP() *http.Server {
	h := &http.Server{Handler: s, ReadHeaderTimeout: readHeaderTimeout}
	h.RegisterOnShutdown(s.StopStreams)
	return h
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
