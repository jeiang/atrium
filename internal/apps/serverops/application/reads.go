package application

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	grafanatools "github.com/jeiang/atrium/internal/apps/serverops/grafana"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/tool"
)

type QueryInput struct {
	DatasourceUID string
	Expr          string
	Kind          string
	Start         *string
	End           *string
	Step          *string
	Limit         int64
}

type AlertsInput struct{}

type SelfDiagnoseInput struct {
	Scope   string
	Minutes int64
}

func (s *Service) Query(ctx context.Context, in QueryInput) (application.Read[any], error) {
	out, err := s.query(ctx, in)
	return application.Read[any]{Data: out}, err
}

func (s *Service) Alerts(ctx context.Context, _ AlertsInput) (application.Read[map[string]any], error) {
	out, err := s.alerts(ctx)
	return application.Read[map[string]any]{Data: out}, err
}

func (s *Service) SelfDiagnose(ctx context.Context, in SelfDiagnoseInput) (application.Read[map[string]any], error) {
	out, err := s.selfDiagnose(ctx, in)
	return application.Read[map[string]any]{Data: out}, err
}

var defaultLogQueries = map[string]string{
	grafanatools.Loki:         `{unit=~"atrium.*"} |~ "(?i)(error|warn)"`,
	grafanatools.VictoriaLogs: `_stream:{unit=~"atrium.*"} (error OR warn)`,
}

var selfMetrics = []struct{ ref, expr string }{
	{"stale_jobs", `atrium_job_stale == 1`},
	{"last_success_age_seconds", `time() - atrium_job_last_success_timestamp_seconds`},
	{"budget_spent_ratio", `atrium_budget_spent_ratio`},
	{"lock_timeouts_1h", `increase(atrium_db_exclusive_timeouts_total[1h])`},
}

func (s *Service) query(ctx context.Context, in QueryInput) (any, error) {
	uid, expr, kind := in.DatasourceUID, in.Expr, in.Kind
	if kind == "" {
		kind = "prometheus"
	}
	if !slices.Contains(kinds, kind) {
		return nil, tool.Error("kind must be one of " + strings.Join(kinds, ", "))
	}
	lim, err := grafanatools.ReadLimits(ctx, s.db.Read)
	if err != nil {
		return nil, err
	}
	if uid == "" {
		if uid, err = s.defaultDatasource(ctx, kind); err != nil {
			return nil, err
		}
	}
	limit := in.Limit
	q := map[string]any{"refId": "A", "datasource": map[string]any{"uid": uid}, "expr": expr, "maxDataPoints": int64(lim.MetricRows)}
	var rowCap int
	if kind == "prometheus" {
		q["range"] = true
		q["instant"] = false
		if in.Step != nil {
			q["interval"] = *in.Step
		}
		rowCap = int(min(limit, int64(lim.MetricRows)))
	} else {
		q["queryType"] = "range"
		q["maxLines"] = limit
		rowCap = int(min(limit, int64(lim.LogRows)))
	}
	start, end := in.Start, in.End
	if start == nil {
		start = new("now-1h")
	}
	if end == nil {
		end = new("now")
	}
	body := map[string]any{"queries": []any{q}, "from": *start, "to": *end}
	payload, err := grafanatools.Query(ctx, s.db.Read, lim, http.MethodPost, "/api/ds/query", "", body)
	if err != nil {
		return nil, err
	}
	return compactResults(payload, rowCap, lim)
}

func (s *Service) alerts(ctx context.Context) (map[string]any, error) {
	lim, err := grafanatools.ReadLimits(ctx, s.db.Read)
	if err != nil {
		return nil, err
	}
	foundValue, err := grafanatools.Query(ctx, s.db.Read, lim, http.MethodGet, "/api/prometheus/grafana/api/v1/alerts", "", nil)
	if err != nil {
		return nil, err
	}
	found, err := attr(foundValue)
	if err != nil {
		return nil, err
	}
	var list []any
	if data := get(found, "data"); truthy(data) {
		dd, err := attr(data)
		if err != nil {
			return nil, err
		}
		if list, err = items(orDefault(dd, "alerts", []any{})); err != nil {
			return nil, err
		}
	}
	var firing []map[string]any
	for _, v := range list {
		a, err := attr(v)
		if err != nil {
			return nil, err
		}
		if state := strings.ToLower(pyStr(orDefault(a, "state", ""))); state == "firing" || state == "alerting" {
			firing = append(firing, a)
		}
	}
	out := make([]any, 0, min(len(firing), lim.Alerts))
	for _, a := range firing[:min(len(firing), lim.Alerts)] {
		var summary any = ""
		if ann := get(a, "annotations"); truthy(ann) {
			ad, err := attr(ann)
			if err != nil {
				return nil, err
			}
			summary = orDefault(ad, "summary", "")
		}
		text, ok := summary.(string)
		if !ok {
			return nil, tool.Error("grafana returned an unexpected value")
		}
		out = append(out, map[string]any{
			"labels":    orDefault(a, "labels", map[string]any{}),
			"summary":   tool.Clip(text, lim.CellChars),
			"active_at": get(a, "activeAt"),
			"value":     get(a, "value"),
		})
	}
	return map[string]any{"total_firing": int64(len(firing)), "alerts": out}, nil
}

func (s *Service) selfDiagnose(ctx context.Context, in SelfDiagnoseInput) (map[string]any, error) {
	scope, minutes := in.Scope, in.Minutes
	if scope == "" {
		scope = "both"
	}
	if scope != "metrics" && scope != "logs" && scope != "both" {
		return nil, tool.Error("scope must be one of metrics, logs, both")
	}
	lim, err := grafanatools.ReadLimits(ctx, s.db.Read)
	if err != nil {
		return nil, err
	}
	access, err := grafanatools.Load(ctx, s.db.Read)
	if err != nil {
		var missing config.NotConfigured
		if errors.As(err, &missing) {
			return nil, tool.NotConfigured(err.Error())
		}
		return nil, err
	}
	metricsUID, logsUID, logsType, logsQuery := access.MetricsUID, access.LogsUID, access.LogsType, access.LogsQuery
	if logsType == "" {
		logsType = grafanatools.Loki
	}
	from := "now-" + strconv.FormatInt(minutes, 10) + "m"
	out := map[string]any{"untrusted": true}
	if scope != "logs" {
		if metricsUID == "" {
			return nil, tool.NotConfigured(grafanatools.KeyMetricsUID + " is not set")
		}
		var queries []any
		for _, m := range selfMetrics {
			queries = append(queries, map[string]any{"refId": m.ref, "datasource": map[string]any{"uid": metricsUID}, "expr": m.expr, "instant": true, "range": false})
		}
		payload, err := grafanatools.Query(ctx, s.db.Read, lim, http.MethodPost, "/api/ds/query", "", map[string]any{"queries": queries, "from": from, "to": "now"})
		if err != nil {
			return nil, err
		}
		metrics, err := compactResults(payload, lim.MetricRows, lim)
		if err != nil {
			return nil, err
		}
		out["metrics"] = metrics
	}
	if scope != "metrics" {
		if logsUID == "" {
			return nil, tool.NotConfigured(grafanatools.KeyLogsUID + " is not set")
		}
		if logsQuery == "" {
			logsQuery = defaultLogQueries[logsType]
		}
		q := map[string]any{"refId": "logs", "datasource": map[string]any{"uid": logsUID}, "expr": logsQuery, "queryType": "range", "maxLines": int64(lim.LogRows)}
		payload, err := grafanatools.Query(ctx, s.db.Read, lim, http.MethodPost, "/api/ds/query", "", map[string]any{"queries": []any{q}, "from": from, "to": "now"})
		if err != nil {
			return nil, err
		}
		logs, err := compactResults(payload, lim.LogRows, lim)
		if err != nil {
			return nil, err
		}
		out["logs_dialect"] = logsType
		out["logs"] = logs
	}
	return out, nil
}

func (s *Service) defaultDatasource(ctx context.Context, kind string) (string, error) {
	access, err := grafanatools.Load(ctx, s.db.Read)
	if err != nil {
		var missing config.NotConfigured
		if errors.As(err, &missing) {
			return "", tool.NotConfigured(err.Error())
		}
		return "", err
	}
	if kind == "logs" {
		if access.LogsUID == "" {
			return "", tool.NotConfigured(grafanatools.KeyLogsUID + " is not set")
		}
		return access.LogsUID, nil
	}
	if access.MetricsUID == "" {
		return "", tool.NotConfigured(grafanatools.KeyMetricsUID + " is not set")
	}
	return access.MetricsUID, nil
}
