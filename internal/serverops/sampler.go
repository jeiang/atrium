package serverops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/module"
)

const (
	retentionDays = 14
	factTTLHours  = 24
)

func Callables() map[string]worker.Callable {
	return map[string]worker.Callable{
		scheduler.OpsSamplerName: {
			Modules: []string{module.Grafana},
			Run: func(ctx context.Context, e worker.Env) (worker.Result, error) {
				if err := Sample(ctx, e.Service.DB, e.Service.Env, e.Now()); err != nil {
					return worker.Result{}, err
				}
				return worker.Result{}, Supervise(ctx, e)
			},
		},
	}
}

func Sample(ctx context.Context, d *db.DB, env config.Env, now float64) error {
	set := assistant.Read(ctx, d.Read)
	hosts := SplitHosts(set.Text(KeyHosts))
	queries := map[string]string{
		MetricUp:          defaultQueryUp,
		MetricCPU:         defaultQueryCPU,
		MetricMemory:      defaultQueryMemory,
		MetricDisk:        defaultQueryDisk,
		MetricFailedUnits: defaultQueryFailedUnit,
	}
	uid := set.Text(grafanatools.KeyMetricsUID)
	if err := set.Err(); err != nil {
		return err
	}
	if len(hosts) == 0 {
		return nil
	}
	if uid == "" {
		return config.NotConfigured(grafanatools.KeyMetricsUID + " must be set")
	}
	lim, err := grafanatools.ReadLimits(ctx, d.Read)
	if err != nil {
		return err
	}
	loc, err := config.From(ctx).Location(env)
	if err != nil {
		return err
	}
	stamp := time.Unix(int64(now), 0).In(loc).Format("15:04")
	for _, host := range hosts {
		for _, metric := range Metrics[:len(Metrics)-1] {
			expr := strings.ReplaceAll(queries[metric], "{{host}}", host)
			payload, err := grafanatools.Query(ctx, d.Read, lim, http.MethodPost, "/api/ds/query", "", map[string]any{
				"queries": []any{map[string]any{
					"refId": "A", "datasource": map[string]any{"uid": uid}, "expr": expr, "instant": true, "range": false,
				}},
				"from": "now-5m", "to": "now",
			})
			if err != nil {
				return err
			}
			value, found, err := instantValue(payload)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			var state *string
			if metric == MetricUp {
				s := "down"
				if value != 0 {
					s = "up"
				}
				state = &s
			}
			if err := record(ctx, d, now, host, metric, &value, state, nil); err != nil {
				return err
			}
			if err := fact(ctx, d, now, host, metric, stamp, value, state); err != nil {
				return err
			}
		}
	}
	alerts, err := firingAlerts(ctx, d, lim, "instance", hosts)
	if err != nil {
		return err
	}
	for _, host := range hosts {
		names := alerts[host]
		sort.Strings(names)
		detail, err := json.Marshal(map[string]any{"alerts": names})
		if err != nil {
			return err
		}
		value := float64(len(names))
		if err := record(ctx, d, now, host, MetricAlerts, &value, nil, new(string(detail))); err != nil {
			return err
		}
		if err := fact(ctx, d, now, host, MetricAlerts, stamp, value, nil); err != nil {
			return err
		}
	}
	_, err = d.Write.ExecContext(ctx, "DELETE FROM ops_samples WHERE ts < ?", now-retentionDays*24*3600)
	return err
}

func SplitHosts(value string) []string {
	var out []string
	for _, host := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if host != "" && !slices.Contains(out, host) {
			out = append(out, host)
		}
	}
	return out
}

func record(ctx context.Context, d *db.DB, now float64, host, metric string, value *float64, state *string, detail *string) error {
	_, err := d.Write.ExecContext(ctx,
		"INSERT INTO ops_samples (ts, host, metric, value, state, detail) VALUES (?, ?, ?, ?, ?, ?)",
		now, host, metric, value, state, detail)
	return err
}

func fact(ctx context.Context, d *db.DB, now float64, host, metric, stamp string, value float64, state *string) error {
	title := host + " " + metric
	body := fmt.Sprintf("%s at %s", formatMetric(metric, value, state), stamp)
	_, err := kb.UpsertFact(ctx, d, "ops:"+host+":"+metric, title, body, now+factTTLHours*3600, now)
	return err
}

func formatMetric(metric string, value float64, state *string) string {
	switch metric {
	case MetricUp:
		if state != nil {
			return *state
		}
		if value != 0 {
			return "up"
		}
		return "down"
	case MetricCPU, MetricMemory, MetricDisk:
		return fmt.Sprintf("%.1f%%", value)
	case MetricFailedUnits:
		return fmt.Sprintf("%.0f failed", value)
	case MetricAlerts:
		return fmt.Sprintf("%.0f firing", value)
	}
	return fmt.Sprintf("%v", value)
}

func instantValue(payload any) (float64, bool, error) {
	root, ok := payload.(map[string]any)
	if !ok {
		return 0, false, errors.New("ops: grafana returned an unexpected query payload")
	}
	results, _ := root["results"].(map[string]any)
	for _, raw := range results {
		result, _ := raw.(map[string]any)
		if e, ok := result["error"]; ok && e != nil {
			return 0, false, fmt.Errorf("ops: grafana query error: %v", e)
		}
		frames, _ := result["frames"].([]any)
		for _, rawFrame := range frames {
			frame, _ := rawFrame.(map[string]any)
			data, _ := frame["data"].(map[string]any)
			values, _ := data["values"].([]any)
			if len(values) < 2 {
				continue
			}
			column, _ := values[1].([]any)
			if len(column) == 0 {
				continue
			}
			value, ok := numeric(column[0])
			if !ok {
				continue
			}
			return value, true, nil
		}
	}
	return 0, false, nil
}

func numeric(value any) (float64, bool) {
	var f float64
	switch v := value.(type) {
	case float64:
		f = v
	case int64:
		f = float64(v)
	case int:
		f = float64(v)
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func firingAlerts(ctx context.Context, d *db.DB, lim grafanatools.Limits, label string, hosts []string) (map[string][]string, error) {
	if label == "" {
		label = "instance"
	}
	payload, err := grafanatools.Query(ctx, d.Read, lim, http.MethodGet, "/api/prometheus/grafana/api/v1/alerts", "", nil)
	if err != nil {
		return nil, err
	}
	root, ok := payload.(map[string]any)
	if !ok {
		return nil, errors.New("ops: grafana returned an unexpected alerts payload")
	}
	data, _ := root["data"].(map[string]any)
	list, _ := data["alerts"].([]any)
	out := map[string][]string{}
	for _, raw := range list {
		alert, _ := raw.(map[string]any)
		state := strings.ToLower(fmt.Sprint(alert["state"]))
		if state != "firing" && state != "alerting" {
			continue
		}
		labels, _ := alert["labels"].(map[string]any)
		instance := fmt.Sprint(labels[label])
		name := fmt.Sprint(labels["alertname"])
		if name == "" || name == "<nil>" {
			name = "alert"
		}
		for _, host := range hosts {
			if instance == host || strings.HasPrefix(instance, host+":") {
				out[host] = append(out[host], name)
			}
		}
	}
	return out, nil
}
