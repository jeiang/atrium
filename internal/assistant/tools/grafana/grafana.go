package grafanatools

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/tool"
)

type Limits struct {
	Timeout    time.Duration
	Series     int
	MetricRows int
	LogRows    int
	Alerts     int
	Dashboards int
	CellChars  int
}

func ReadLimits(_ context.Context, _ db.Querier) (Limits, error) {
	return Limits{
		Timeout:    time.Duration(assistant.TuningAgentGrafanaTimeoutSeconds) * time.Second,
		Series:     assistant.TuningAgentGrafanaMaxSeries,
		MetricRows: assistant.TuningAgentGrafanaMaxMetricRows,
		LogRows:    assistant.TuningAgentGrafanaMaxLogRows,
		Alerts:     assistant.TuningAgentGrafanaMaxAlerts,
		Dashboards: assistant.TuningAgentGrafanaMaxDashboards,
		CellChars:  assistant.TuningAgentGrafanaCellChars,
	}, nil
}

func errorClass(err error) string {
	var op *net.OpError
	var cert x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	var dns *net.DNSError
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "protocol error"
	case errors.As(err, &op) && op.Op == "dial":
		if op.Timeout() {
			return "connect timeout"
		}
		return "connect failed"
	case errors.As(err, &cert), errors.As(err, &host), errors.As(err, &invalid), errors.As(err, &dns):
		return "connect failed"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() && !errors.As(err, &op) {
		return "connect timeout"
	}
	var timeouter interface{ Timeout() bool }
	if errors.As(err, &timeouter) && timeouter.Timeout() {
		return "read timeout"
	}
	return "read failed"
}

func urlClass(target string) string {
	scheme, _, found := strings.Cut(target, ":")
	if !found || !strings.EqualFold(scheme, "http") && !strings.EqualFold(scheme, "https") {
		return "unsupported protocol"
	}
	if u, err := url.Parse(target); err != nil || u.Host == "" {
		return "connect failed"
	}
	return ""
}

func Query(ctx context.Context, q db.Querier, lim Limits, method, path, rawQuery string, body any) (any, error) {
	if method != http.MethodGet && (method != http.MethodPost || path != "/api/ds/query") {
		return nil, tool.Error("grafana tools are read-only")
	}
	access, err := Load(ctx, q)
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
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = strings.NewReader(string(encoded))
	}
	if class := urlClass(target); class != "" {
		return nil, tool.Error("grafana unreachable: " + class)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, tool.Error("grafana unreachable: connect failed")
	}
	req.Header.Set("Authorization", "Bearer "+access.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := access.HTTP
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
		return nil, tool.Error("grafana " + strconv.Itoa(resp.StatusCode) + ": " + tool.Clip(strings.ToValidUTF8(string(raw), "\ufffd"), lim.CellChars))
	}
	value, err := jsonx.Loads(raw)
	if err != nil {
		return nil, tool.Error("grafana returned non-JSON")
	}
	return value, nil
}
