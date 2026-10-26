package grafanatools

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/integrations/transport"
	"github.com/jeiang/atrium/internal/py"
)

type limits struct {
	timeout    time.Duration
	series     int
	metricRows int
	logRows    int
	alerts     int
	dashboards int
	cellChars  int
}

func readLimits(ctx context.Context, tc *tool.Context) (limits, error) {
	r := assistant.Read(ctx, tc.DB.Read)
	lim := limits{
		timeout:    time.Duration(r.Int(assistant.KeyGrafanaTimeout)) * time.Second,
		series:     r.Int(assistant.KeyGrafanaSeries),
		metricRows: r.Int(assistant.KeyGrafanaMetricRows),
		logRows:    r.Int(assistant.KeyGrafanaLogRows),
		alerts:     r.Int(assistant.KeyGrafanaAlerts),
		dashboards: r.Int(assistant.KeyGrafanaDashboards),
		cellChars:  r.Int(assistant.KeyGrafanaCellChars),
	}
	return lim, r.Err()
}

var (
	kinds      = []string{"prometheus", "logs"}
	dropFields = []string{"id", "tsNs"}
	contexts   = tool.Kinds{tool.Chat, tool.Cron, tool.Sub}
)

func errorClass(err error) string {
	var op *net.OpError
	var cert x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	var dns *net.DNSError
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "RemoteProtocolError"
	case strings.Contains(err.Error(), "TLS handshake timeout"):
		return "ConnectTimeout"
	case errors.As(err, &op) && op.Op == "dial":
		if op.Timeout() {
			return "ConnectTimeout"
		}
		return "ConnectError"
	case errors.As(err, &cert), errors.As(err, &host), errors.As(err, &invalid), errors.As(err, &dns):
		return "ConnectError"
	}
	var timeouter interface{ Timeout() bool }
	if errors.As(err, &timeouter) && timeouter.Timeout() {
		return "ReadTimeout"
	}
	return "ReadError"
}

func urlClass(target string) string {
	scheme, _, found := strings.Cut(target, ":")
	if !found || !strings.EqualFold(scheme, "http") && !strings.EqualFold(scheme, "https") {
		return "UnsupportedProtocol"
	}
	if u, err := url.Parse(target); err != nil || u.Host == "" {
		return "ConnectError"
	}
	return ""
}

func request(ctx context.Context, tc *tool.Context, lim limits, method, path, rawQuery string, body any) (any, error) {
	if method != http.MethodGet && (method != http.MethodPost || path != "/api/ds/query") {
		return nil, tool.Error("grafana tools are read-only")
	}
	access, err := Load(ctx, tc.DB.Read)
	var missing config.NotConfigured
	if errors.As(err, &missing) {
		return nil, tool.NotConfigured(err.Error())
	}
	if err != nil {
		return nil, err
	}
	target := access.URL + path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(py.JSONDumps(body, py.JSONOpts{NoEnsureASCII: true, Compact: true}))
	}
	if class := urlClass(target); class != "" {
		return nil, tool.Error("grafana unreachable: " + class)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, tool.Error("grafana unreachable: ConnectError")
	}
	req.Header.Set("Authorization", "Bearer "+access.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client, err := transport.For(ctx, tc.DB.Read, "grafana", lim.timeout)
	var invalid transport.Error
	if errors.As(err, &invalid) {
		return nil, tool.Error("grafana unreachable: " + invalid.Error())
	}
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, tool.Error("grafana unreachable: " + errorClass(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, tool.Error("grafana unreachable: " + errorClass(err))
	}
	if resp.StatusCode >= 400 {
		return nil, tool.Error("grafana " + strconv.Itoa(resp.StatusCode) + ": " + tool.Clip(strings.ToValidUTF8(string(raw), "\ufffd"), lim.cellChars))
	}
	value, err := py.JSONLoads(raw)
	if err != nil {
		return nil, tool.Error("grafana returned non-JSON")
	}
	return value, nil
}

func attr(v any) (py.Dict, error) {
	d, ok := v.(py.Dict)
	if !ok {
		return nil, &tool.Raised{Class: "AttributeError"}
	}
	return d, nil
}

func items(v any) ([]any, error) {
	l, ok := v.([]any)
	if !ok {
		return nil, &tool.Raised{Class: "TypeError"}
	}
	return l, nil
}

func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case int64:
		return v != 0
	case *big.Int:
		return v.Sign() != 0
	case float64:
		return v != 0
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case py.Dict:
		return len(v) > 0
	}
	return true
}

func get(d py.Dict, key string) any {
	v, _ := d.Get(key)
	return v
}

func orDefault(d py.Dict, key string, def any) any {
	if v, ok := d.Get(key); ok {
		return v
	}
	return def
}

func pyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return py.ReprValue(v)
}

func cell(v any, cellChars int) any {
	switch v := v.(type) {
	case float64:
		return py.Round(v, 6)
	case string:
		return tool.Clip(v, cellChars)
	case py.Dict, []any:
		return tool.Clip(py.ReprValue(v), cellChars)
	}
	return v
}

func timeCell(v any, cellChars int) any {
	var ms float64
	switch n := v.(type) {
	case int64:
		ms = float64(n)
	case *big.Int:
		ms, _ = new(big.Float).SetInt(n).Float64()
	case float64:
		ms = n
	default:
		return cell(v, cellChars)
	}
	if !(ms > 1e11) {
		return cell(v, cellChars)
	}
	sec, us := py.SplitTimestamp(ms / 1000)
	return py.ISOFormat(time.Unix(sec, us*1000).UTC(), "seconds")
}

func sample(count, limit int) ([]int, error) {
	out := make([]int, 0, min(count, limit))
	if count <= limit {
		for i := range count {
			out = append(out, i)
		}
		return out, nil
	}
	if limit == 1 {
		return nil, &tool.Raised{Class: "ZeroDivisionError"}
	}
	for i := range limit {
		out = append(out, int(math.RoundToEven(float64(i*(count-1))/float64(limit-1))))
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func compactFrame(frameValue any, rowCap, cellChars int) (py.Dict, error) {
	frame, err := attr(frameValue)
	if err != nil {
		return nil, err
	}
	schema, err := attr(orDefault(frame, "schema", py.Dict{}))
	if err != nil {
		return nil, err
	}
	fieldList, err := items(orDefault(schema, "fields", []any{}))
	if err != nil {
		return nil, err
	}
	data, err := attr(orDefault(frame, "data", py.Dict{}))
	if err != nil {
		return nil, err
	}
	columns, err := items(orDefault(data, "values", []any{}))
	if err != nil {
		return nil, err
	}
	fields := make([]py.Dict, len(fieldList))
	var keep []int
	for i, f := range fieldList {
		if fields[i], err = attr(f); err != nil {
			return nil, err
		}
		name, named := get(fields[i], "name").(string)
		if i < len(columns) && (!named || !slices.Contains(dropFields, name)) {
			keep = append(keep, i)
		}
	}
	total := 0
	if len(columns) > 0 {
		first, err := items(columns[0])
		if err != nil {
			return nil, err
		}
		total = len(first)
	}
	picked, err := sample(total, rowCap)
	if err != nil {
		return nil, err
	}
	rows := make([]any, 0, len(picked))
	for _, r := range picked {
		row := make([]any, 0, len(keep))
		for _, i := range keep {
			column, err := items(columns[i])
			if err != nil {
				return nil, err
			}
			if r >= len(column) {
				return nil, &tool.Raised{Class: "IndexError"}
			}
			if get(fields[i], "type") == "time" {
				row = append(row, timeCell(column[r], cellChars))
			} else {
				row = append(row, cell(column[r], cellChars))
			}
		}
		rows = append(rows, row)
	}
	var labels py.Dict
	names := make([]any, 0, len(keep))
	for _, i := range keep {
		if l := get(fields[i], "labels"); truthy(l) {
			ld, err := attr(l)
			if err != nil {
				return nil, err
			}
			for _, p := range ld {
				labels = labels.Set(p.Key, p.Value)
			}
		}
		names = append(names, orDefault(fields[i], "name", ""))
	}
	out := py.Dict{{Key: "fields", Value: names}, {Key: "rows", Value: rows}, {Key: "total_rows", Value: int64(total)}}
	if name := get(schema, "name"); truthy(name) {
		out = append(out, py.Pair{Key: "name", Value: name})
	}
	if len(labels) > 0 {
		out = append(out, py.Pair{Key: "labels", Value: labels})
	}
	return out, nil
}

func compactResults(payloadValue any, rowCap int, lim limits) (any, error) {
	payload, err := attr(payloadValue)
	if err != nil {
		return nil, err
	}
	var frames []any
	if results := get(payload, "results"); truthy(results) {
		rd, err := attr(results)
		if err != nil {
			return nil, err
		}
		for _, p := range rd {
			result, err := attr(p.Value)
			if err != nil {
				return nil, err
			}
			if e := get(result, "error"); truthy(e) {
				return nil, tool.Error("query error: " + tool.Clip(pyStr(e), lim.cellChars))
			}
			if f := get(result, "frames"); truthy(f) {
				list, err := items(f)
				if err != nil {
					return nil, err
				}
				frames = append(frames, list...)
			}
		}
	}
	series := make([]any, 0, min(len(frames), lim.series))
	for _, f := range frames[:min(len(frames), lim.series)] {
		c, err := compactFrame(f, rowCap, lim.cellChars)
		if err != nil {
			return nil, err
		}
		series = append(series, c)
	}
	return py.Dict{{Key: "series", Value: series}, {Key: "total_series", Value: int64(len(frames))}}, nil
}

func datasources(ctx context.Context, tc *tool.Context, _ py.Dict) (any, error) {
	lim, err := readLimits(ctx, tc)
	if err != nil {
		return nil, err
	}
	found, err := request(ctx, tc, lim, http.MethodGet, "/api/datasources", "", nil)
	if err != nil {
		return nil, err
	}
	list, err := items(found)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(list))
	for _, v := range list {
		d, err := attr(v)
		if err != nil {
			return nil, err
		}
		out = append(out, py.Dict{{Key: "uid", Value: get(d, "uid")}, {Key: "name", Value: get(d, "name")}, {Key: "type", Value: get(d, "type")}})
	}
	return py.Dict{{Key: "datasources", Value: out}}, nil
}

func query(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	uid, _, err := tool.TextArg(args, "datasource_uid", true)
	if err != nil {
		return nil, err
	}
	expr, _, err := tool.TextArg(args, "expr", true)
	if err != nil {
		return nil, err
	}
	kind, ok, err := tool.TextArg(args, "kind", false)
	if err != nil {
		return nil, err
	}
	if !ok {
		kind = "prometheus"
	}
	if !slices.Contains(kinds, kind) {
		return nil, tool.Error("kind must be one of " + strings.Join(kinds, ", "))
	}
	lim, err := readLimits(ctx, tc)
	if err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", 100, 200, 1)
	if err != nil {
		return nil, err
	}
	q := py.Dict{
		{Key: "refId", Value: "A"},
		{Key: "datasource", Value: py.Dict{{Key: "uid", Value: uid}}},
		{Key: "expr", Value: expr},
		{Key: "maxDataPoints", Value: int64(lim.metricRows)},
	}
	var rowCap int
	if kind == "prometheus" {
		q = q.Set("range", true).Set("instant", false)
		if step, ok, err := tool.TextArg(args, "step", false); err != nil {
			return nil, err
		} else if ok {
			q = q.Set("interval", step)
		}
		rowCap = int(min(limit, int64(lim.metricRows)))
	} else {
		q = q.Set("queryType", "range").Set("maxLines", limit)
		rowCap = int(min(limit, int64(lim.logRows)))
	}
	start, ok, err := tool.TextArg(args, "start", false)
	if err != nil {
		return nil, err
	}
	if !ok {
		start = "now-1h"
	}
	end, ok, err := tool.TextArg(args, "end", false)
	if err != nil {
		return nil, err
	}
	if !ok {
		end = "now"
	}
	body := py.Dict{{Key: "queries", Value: []any{q}}, {Key: "from", Value: start}, {Key: "to", Value: end}}
	payload, err := request(ctx, tc, lim, http.MethodPost, "/api/ds/query", "", body)
	if err != nil {
		return nil, err
	}
	return compactResults(payload, rowCap, lim)
}

func alerts(ctx context.Context, tc *tool.Context, _ py.Dict) (any, error) {
	lim, err := readLimits(ctx, tc)
	if err != nil {
		return nil, err
	}
	foundValue, err := request(ctx, tc, lim, http.MethodGet, "/api/prometheus/grafana/api/v1/alerts", "", nil)
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
	var firing []py.Dict
	for _, v := range list {
		a, err := attr(v)
		if err != nil {
			return nil, err
		}
		if state := strings.ToLower(pyStr(orDefault(a, "state", ""))); state == "firing" || state == "alerting" {
			firing = append(firing, a)
		}
	}
	out := make([]any, 0, min(len(firing), lim.alerts))
	for _, a := range firing[:min(len(firing), lim.alerts)] {
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
			return nil, &tool.Raised{Class: "TypeError"}
		}
		out = append(out, py.Dict{
			{Key: "labels", Value: orDefault(a, "labels", py.Dict{})},
			{Key: "summary", Value: tool.Clip(text, lim.cellChars)},
			{Key: "active_at", Value: get(a, "activeAt")},
			{Key: "value", Value: get(a, "value")},
		})
	}
	return py.Dict{{Key: "total_firing", Value: int64(len(firing))}, {Key: "alerts", Value: out}}, nil
}

func searchDashboards(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	text, _, err := tool.TextArg(args, "query", false)
	if err != nil {
		return nil, err
	}
	lim, err := readLimits(ctx, tc)
	if err != nil {
		return nil, err
	}
	rawQuery := "query=" + url.QueryEscape(text) + "&type=dash-db&limit=" + strconv.Itoa(lim.dashboards)
	found, err := request(ctx, tc, lim, http.MethodGet, "/api/search", rawQuery, nil)
	if err != nil {
		return nil, err
	}
	list, err := items(found)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, min(len(list), lim.dashboards))
	for _, v := range list[:min(len(list), lim.dashboards)] {
		d, err := attr(v)
		if err != nil {
			return nil, err
		}
		out = append(out, py.Dict{
			{Key: "title", Value: get(d, "title")},
			{Key: "uid", Value: get(d, "uid")},
			{Key: "url", Value: get(d, "url")},
			{Key: "folder", Value: get(d, "folderTitle")},
			{Key: "tags", Value: orDefault(d, "tags", []any{})},
		})
	}
	return py.Dict{{Key: "dashboards", Value: out}}, nil
}

const emptySchema = `{"type": "object", "properties": {}, "required": [], "additionalProperties": false}`

func Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name:        "grafana_datasources",
			Description: "List Grafana datasources (uid, name, type).",
			InputSchema: json.RawMessage(emptySchema),
			Handler:     datasources,
			Effect:      tool.Read,
			Contexts:    contexts,
		},
		{
			Name:        "grafana_query",
			Description: "Run a read-only PromQL (kind=prometheus) or LogQL (kind=logs) query via Grafana. Results are compacted to small tables.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"datasource_uid": {"type": "string"}, "expr": {"type": "string"}, ` +
				`"kind": {"type": "string", "enum": ["prometheus", "logs"]}, "start": {"type": "string", "description": "e.g. now-1h or epoch ms"}, ` +
				`"end": {"type": "string"}, "step": {"type": "string", "description": "Prometheus step, e.g. 60s"}, ` +
				`"limit": {"type": "integer", "minimum": 1, "maximum": 200}}, "required": ["datasource_uid", "expr"], "additionalProperties": false}`),
			Handler:  query,
			Effect:   tool.Read,
			Contexts: contexts,
		},
		{
			Name:        "grafana_alerts",
			Description: "List currently firing Grafana alerts.",
			InputSchema: json.RawMessage(emptySchema),
			Handler:     alerts,
			Effect:      tool.Read,
			Contexts:    contexts,
		},
		{
			Name:        "grafana_search_dashboards",
			Description: "Search Grafana dashboards by title.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"query": {"type": "string"}}, "required": [], "additionalProperties": false}`),
			Handler:     searchDashboards,
			Effect:      tool.Read,
			Contexts:    contexts,
		},
		selfDiagnoseTool,
	}
}
