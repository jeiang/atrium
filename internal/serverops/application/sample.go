package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/serverops"
)

const retentionDays = 14

type SampleInput struct{}

type SampleResult struct {
	Hosts   int `json:"hosts"`
	Samples int `json:"samples"`
}

type sampleRow struct {
	host, metric string
	value        float64
	state        *string
	detail       *string
}

func (s *Service) Sample(ctx context.Context, _ SampleInput) (application.Change[SampleResult], error) {
	rows, hosts, err := s.collect(ctx)
	if err != nil {
		outcome := state.Failed
		var missing config.NotConfigured
		if errors.As(err, &missing) {
			outcome = state.Refused
		}
		if auditErr := s.auditAlone(ctx, SampleName, "ops_samples", outcome); auditErr != nil {
			return application.Change[SampleResult]{}, errors.Join(err, auditErr)
		}
		return application.Change[SampleResult]{}, err
	}
	result := SampleResult{Hosts: hosts, Samples: len(rows)}
	if hosts == 0 {
		auditID, err := s.writeAudit(ctx, state.Noop)
		return application.Change[SampleResult]{Data: result, AuditID: auditID}, err
	}
	now := s.clock()
	var auditID int64
	err = s.db.WithTx(ctx, func(tx *sql.Tx) error {
		for _, row := range rows {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO ops_samples (ts, host, metric, value, state, detail) VALUES (?, ?, ?, ?, ?, ?)",
				now, row.host, row.metric, row.value, row.state, row.detail); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM ops_samples WHERE ts < ?", now-retentionDays*24*3600); err != nil {
			return err
		}
		var err error
		auditID, err = s.state.Audit(ctx, tx, state.Entry{Operation: string(SampleName), Outcome: state.OK, Target: "ops_samples"})
		return err
	})
	if err != nil {
		return application.Change[SampleResult]{}, err
	}
	return application.Change[SampleResult]{Data: result, AuditID: auditID}, nil
}

func (s *Service) writeAudit(ctx context.Context, outcome state.Outcome) (int64, error) {
	var id int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = s.state.Audit(ctx, tx, state.Entry{Operation: string(SampleName), Outcome: outcome, Target: "ops_samples"})
		return err
	})
	return id, err
}

func (s *Service) collect(ctx context.Context) ([]sampleRow, int, error) {
	host := serverops.HostValues(ctx)
	hosts, selector, queries := host.Hosts, host.Selector, host.Queries
	if len(hosts) == 0 {
		return nil, 0, nil
	}
	access, err := grafanatools.Load(ctx, s.db.Read)
	if err != nil {
		return nil, 0, err
	}
	uid := access.MetricsUID
	if uid == "" {
		return nil, 0, config.NotConfigured("the Grafana connection has no metrics datasource UID")
	}
	lim, err := grafanatools.ReadLimits(ctx, s.db.Read)
	if err != nil {
		return nil, 0, err
	}
	var rows []sampleRow
	for _, host := range hosts {
		for _, metric := range serverops.Metrics[:len(serverops.Metrics)-1] {
			expr := strings.ReplaceAll(queries[metric], "{{selector}}", selector)
			expr = strings.ReplaceAll(expr, "{{host}}", host)
			payload, err := grafanatools.Query(ctx, s.db.Read, lim, http.MethodPost, "/api/ds/query", "", map[string]any{
				"queries": []any{map[string]any{
					"refId": "A", "datasource": map[string]any{"uid": uid}, "expr": expr, "instant": true, "range": false,
				}},
				"from": "now-5m", "to": "now",
			})
			if err != nil {
				return nil, 0, err
			}
			value, found, err := instantValue(payload)
			if err != nil {
				return nil, 0, err
			}
			if !found {
				continue
			}
			var up *string
			if metric == serverops.MetricUp {
				s := "down"
				if value != 0 {
					s = "up"
				}
				up = &s
			}
			rows = append(rows, sampleRow{host: host, metric: metric, value: value, state: up})
		}
	}
	alerts, err := firingAlerts(ctx, s.db.Read, lim, "instance", hosts)
	if err != nil {
		return nil, 0, err
	}
	for _, host := range hosts {
		names := alerts[host]
		sort.Strings(names)
		detail, err := json.Marshal(map[string]any{"alerts": names})
		if err != nil {
			return nil, 0, err
		}
		rows = append(rows, sampleRow{host: host, metric: serverops.MetricAlerts, value: float64(len(names)), detail: new(string(detail))})
	}
	return rows, len(hosts), nil
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

func firingAlerts(ctx context.Context, q db.Querier, lim grafanatools.Limits, label string, hosts []string) (map[string][]string, error) {
	if label == "" {
		label = "instance"
	}
	payload, err := grafanatools.Query(ctx, q, lim, http.MethodGet, "/api/prometheus/grafana/api/v1/alerts", "", nil)
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
