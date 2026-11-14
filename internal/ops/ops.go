package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	ListenKey     = "ops_listen"
	PprofKey      = "ops_pprof"
	DefaultListen = "127.0.0.1:9180"
	Off           = "off"

	scrapeTTL = 15 * time.Second
	thirdTTL  = 30 * time.Second
	checkWait = 3 * time.Second
)

var log = logx.Logger("atrium.ops")

func HostSettings() []settings.HostDef {
	return []settings.HostDef{
		{Key: ListenKey, Module: "core", Type: settings.String, Default: DefaultListen, Check: checkListen},
		{Key: PprofKey, Module: "core", Type: settings.Bool, Default: "false"},
	}
}

func Checks() []Check {
	return []Check{
		{Name: "db", Critical: true, Run: func(ctx context.Context, d *db.DB) error {
			if err := d.Read.PingContext(ctx); err != nil {
				return err
			}
			return db.RefuseFutureSchema(ctx, d.Read)
		}},
		{Name: "config", Critical: true, Run: func(ctx context.Context, _ *db.DB) error {
			if _, err := config.Load(config.EnvFrom(ctx)); err != nil {
				return err
			}
			_, err := config.From(ctx).Location(config.EnvFrom(ctx))
			return err
		}},
	}
}

func checkListen(value string) error {
	if value == Off {
		return nil
	}
	_, port, err := net.SplitHostPort(value)
	if n, convErr := strconv.Atoi(port); err != nil || convErr != nil || n < 0 || n > 65535 {
		return settings.Invalid("must be host:port, such as " + DefaultListen + ", or off")
	}
	return nil
}

type Sample struct {
	Name   string
	Labels [][2]string
	Value  float64
}

type Metric struct {
	Name    string
	Help    string
	Type    string
	Collect func(ctx context.Context, q db.Querier) ([]Sample, error)
}

type Check struct {
	Name     string
	Critical bool
	Remote   bool
	Run      func(ctx context.Context, d *db.DB) error
}

type server struct {
	d       *db.DB
	env     config.Env
	ctx     context.Context
	metrics []Metric
	checks  []Check
	health  func() runtime.Health

	mu      sync.Mutex
	scraped time.Time
	body    []byte
	third   map[string]result
}

type result struct {
	at  time.Time
	err error
}

func Handler(ctx context.Context, d *db.DB, withPprof bool, env config.Env, metrics []Metric, checks []Check, health func() runtime.Health) http.Handler {
	s := &server{d: d, env: env, ctx: config.WithEnv(ctx, env), metrics: metrics, checks: checks, health: health, third: map[string]result{}}
	mux := http.NewServeMux()
	protocolRoutes(s, withPprof, mux.HandleFunc)
	return mux
}

func protocolRoutes(s *server, withPprof bool, mount func(string, func(http.ResponseWriter, *http.Request))) {
	mount("GET /metrics", s.serveMetrics)
	mount("GET /readyz", s.readyz)
	mount("GET /livez", s.livez)
	mount("GET /healthz", s.healthz)
	if withPprof {
		mount("/debug/pprof/", pprof.Index)
		mount("/debug/pprof/cmdline", pprof.Cmdline)
		mount("/debug/pprof/profile", pprof.Profile)
		mount("/debug/pprof/symbol", pprof.Symbol)
		mount("/debug/pprof/trace", pprof.Trace)
	}
}

func ProtocolRoutes() []string {
	out := make([]string, 0, 9)
	protocolRoutes(nil, true, func(pattern string, _ func(http.ResponseWriter, *http.Request)) {
		if strings.HasPrefix(pattern, "/") {
			pattern = "* " + pattern
		}
		out = append(out, pattern)
	})
	slices.Sort(out)
	return out
}

func (s *server) serveMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	expired := s.body == nil || time.Since(s.scraped) > scrapeTTL
	body := s.body
	s.mu.Unlock()
	if expired {
		body = append(render(s.ctx, s.d, s.metrics), s.readiness(s.ctx)...)
		s.mu.Lock()
		s.body, s.scraped = body, time.Now()
		s.mu.Unlock()
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write(body)
}

func (s *server) readiness(ctx context.Context) []byte {
	var b strings.Builder
	b.WriteString("# HELP atrium_readiness_check_ok 1 when the readiness check passes.\n# TYPE atrium_readiness_check_ok gauge\n")
	for _, c := range s.checks {
		ok := 1
		if s.run(ctx, c) != nil {
			ok = 0
		}
		fmt.Fprintf(&b, "atrium_readiness_check_ok{check=%q} %d\n", c.Name, ok)
	}
	return []byte(b.String())
}

func render(ctx context.Context, d *db.DB, metrics []Metric) []byte {
	var b strings.Builder
	for _, m := range metrics {
		samples, err := m.Collect(ctx, d.Read)
		if err != nil {
			log.Warn("metric collection failed", "metric", m.Name, logx.Err, err.Error())
			continue
		}
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", m.Name, m.Help, m.Name, m.Type)
		for _, sm := range samples {
			name := sm.Name
			if name == "" {
				name = m.Name
			}
			b.WriteString(name)
			if len(sm.Labels) > 0 {
				parts := make([]string, len(sm.Labels))
				for i, l := range sm.Labels {
					parts[i] = l[0] + "=" + strconv.Quote(l[1])
				}
				b.WriteString("{" + strings.Join(parts, ",") + "}")
			}
			b.WriteString(" " + strconv.FormatFloat(sm.Value, 'g', -1, 64) + "\n")
		}
	}
	return []byte(b.String())
}

const (
	StateReady    = "ready"
	StateDegraded = "degraded"
	StateNotReady = "not_ready"
)

func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	state := StateReady
	out := map[string]string{}
	switch h := s.health(); {
	case !h.Started:
		state, out["runtime"] = StateNotReady, "starting"
	case h.Draining:
		state, out["runtime"] = StateNotReady, "draining"
	}
	for _, c := range s.checks {
		err := s.run(r.Context(), c)
		out[c.Name] = "ok"
		if err == nil {
			continue
		}
		out[c.Name] = err.Error()
		if c.Critical {
			state = StateNotReady
		} else if state == StateReady {
			state = StateDegraded
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if state == StateNotReady {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": state, "checks": out})
}

func (s *server) run(_ context.Context, c Check) error {
	if c.Remote {
		s.mu.Lock()
		got, ok := s.third[c.Name]
		s.mu.Unlock()
		if ok && time.Since(got.at) < thirdTTL {
			return got.err
		}
	}
	ctx, cancel := context.WithTimeout(config.WithEnv(s.ctx, s.env), checkWait)
	defer cancel()
	err := c.Run(ctx, s.d)
	if c.Remote {
		s.mu.Lock()
		s.third[c.Name] = result{at: time.Now(), err: err}
		s.mu.Unlock()
	}
	return err
}

func Address(ctx context.Context) string {
	addr := config.From(ctx).HostValue(ListenKey)
	if addr == "" {
		addr = DefaultListen
	}
	if addr == Off {
		return ""
	}
	return addr
}

func Server(ctx context.Context, d *db.DB, env config.Env, metrics []Metric, checks []Check, health func() runtime.Health) *http.Server {
	withPprof := config.From(ctx).HostValue(PprofKey) == "true"
	return &http.Server{Handler: Handler(ctx, d, withPprof, env, metrics, checks, health), ReadHeaderTimeout: 10 * time.Second}
}

func (s *server) livez(w http.ResponseWriter, _ *http.Request) {
	var stalled []string
	for _, svc := range s.health().Services {
		if svc.Critical && svc.Stalled {
			stalled = append(stalled, svc.Name)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if len(stalled) > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "stalled", "services": stalled})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "alive"})
}

type serviceReport struct {
	Name      string         `json:"name"`
	Status    runtime.Status `json:"status"`
	Critical  bool           `json:"critical"`
	Stalled   bool           `json:"stalled"`
	Restarts  int            `json:"restarts"`
	LastError string         `json:"last_error,omitempty"`
}

func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	h := s.health()
	services := make([]serviceReport, 0, len(h.Services))
	for _, svc := range h.Services {
		services = append(services, serviceReport{Name: svc.Name, Status: svc.Status, Critical: svc.Critical, Stalled: svc.Stalled, Restarts: svc.Restarts, LastError: logx.Scrub(svc.LastError)})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"started": h.Started, "draining": h.Draining, "ready": h.Ready, "degraded": h.Degraded, "services": services})
}
