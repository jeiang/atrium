package ops

import (
	"context"
	"encoding/json"
	"errors"
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

func init() {
	settings.MustRegister(
		settings.Def{Key: ListenKey, Module: "core", Type: settings.String, Default: DefaultListen, Check: checkListen, Class: settings.Bootstrap},
		settings.Def{Key: PprofKey, Module: "core", Type: settings.Bool, Default: "false", Class: settings.Bootstrap},
	)
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
	Run      func(ctx context.Context, d *db.DB) error
}

var registry struct {
	mu      sync.Mutex
	metrics []Metric
	checks  []Check
}

func Register(m Metric) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.metrics = append(registry.metrics, m)
}

func Ready(c Check) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.checks = append(registry.checks, c)
}

func snapshot() ([]Metric, []Check) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return slices.Clone(registry.metrics), slices.Clone(registry.checks)
}

type server struct {
	d *db.DB

	mu       sync.Mutex
	scraped  time.Time
	body     []byte
	third    map[string]result
	thirdAge time.Time
}

type result struct {
	at  time.Time
	err error
}

func Handler(d *db.DB, withPprof bool) http.Handler {
	s := &server{d: d, third: map[string]result{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /readyz", s.readyz)
	if withPprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	return mux
}

func (s *server) metrics(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.body == nil || time.Since(s.scraped) > scrapeTTL {
		s.body = render(r.Context(), s.d)
		s.scraped = time.Now()
	}
	body := s.body
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write(body)
}

func render(ctx context.Context, d *db.DB) []byte {
	metrics, _ := snapshot()
	var b strings.Builder
	for _, m := range metrics {
		samples, err := m.Collect(ctx, d.Read)
		if err != nil {
			log.Warn("metric collection failed", "metric", m.Name, logx.Err, err.Error())
			continue
		}
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", m.Name, m.Help, m.Name, m.Type)
		for _, sm := range samples {
			b.WriteString(m.Name)
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
	_, checks := snapshot()
	state := StateReady
	out := map[string]string{}
	for _, c := range checks {
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

func (s *server) run(ctx context.Context, c Check) error {
	if !c.Critical {
		s.mu.Lock()
		got, ok := s.third[c.Name]
		s.mu.Unlock()
		if ok && time.Since(got.at) < thirdTTL {
			return got.err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, checkWait)
	defer cancel()
	err := c.Run(ctx, s.d)
	if !c.Critical {
		s.mu.Lock()
		s.third[c.Name] = result{at: time.Now(), err: err}
		s.mu.Unlock()
	}
	return err
}

func init() {
	Ready(Check{Name: "db", Critical: true, Run: func(ctx context.Context, d *db.DB) error {
		if err := d.Read.PingContext(ctx); err != nil {
			return err
		}
		return db.RefuseFutureSchema(ctx, d.Read)
	}})
	Ready(Check{Name: "config", Critical: true, Run: func(context.Context, *db.DB) error {
		if _, err := config.Load(config.OS); err != nil {
			return err
		}
		_, err := config.Location(config.OS)
		return err
	}})
}

func Start(ctx context.Context, d *db.DB) (stop func(), err error) {
	addr, err := settings.Default.Resolve(ctx, d.Read, ListenKey, 0)
	if err != nil {
		return nil, err
	}
	if addr.Value == Off {
		return func() {}, nil
	}
	withPprof, err := settings.Default.Resolve(ctx, d.Read, PprofKey, 0)
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", addr.Value)
	if err != nil {
		return nil, fmt.Errorf("ops listener: %w", err)
	}
	srv := &http.Server{Handler: Handler(d, withPprof.Value == "true"), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("ops listener stopped", logx.Err, err.Error())
		}
	}()
	log.Info("ops listening", "addr", l.Addr().String())
	return func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(stop)
	}, nil
}
