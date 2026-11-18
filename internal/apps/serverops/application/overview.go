package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/serverops"
	grafanatools "github.com/jeiang/atrium/internal/apps/serverops/grafana"
	"github.com/jeiang/atrium/internal/kb"
)

const OverviewName application.Name = "ops.overview"

const overviewFindings = 20

type OverviewInput struct{}

type ServerOpsPage struct {
	Hosts      []ServerOpsHost    `json:"hosts"`
	Findings   []ServerOpsFinding `json:"findings"`
	GrafanaURL *string            `json:"grafana_url"`
}

type ServerOpsHost struct {
	Host        string   `json:"host"`
	SampledAt   float64  `json:"sampled_at"`
	Up          *bool    `json:"up"`
	CPU         *float64 `json:"cpu_pct"`
	Memory      *float64 `json:"mem_pct"`
	Disk        *float64 `json:"disk_root_pct"`
	FailedUnits *float64 `json:"failed_units"`
	Alerts      []string `json:"alerts"`
}

type ServerOpsFinding struct {
	ID        int64    `json:"id"`
	Fact      string   `json:"fact"`
	Severity  string   `json:"severity" api:"enum=info|warning|critical"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	UpdatedAt float64  `json:"updated_at"`
	ExpiresAt *float64 `json:"expires_at"`
}

func DescribeOverview() application.Definition {
	return application.Describe[OverviewInput, ServerOpsPage](OverviewName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) Overview(ctx context.Context, _ OverviewInput) (application.Read[ServerOpsPage], error) {
	out := ServerOpsPage{Hosts: []ServerOpsHost{}, Findings: []ServerOpsFinding{}}
	samples, err := serverops.LatestSamples(ctx, s.db.Read)
	if err != nil {
		return application.Read[ServerOpsPage]{}, err
	}
	for _, row := range samples {
		if len(out.Hosts) == 0 || out.Hosts[len(out.Hosts)-1].Host != row.Host {
			out.Hosts = append(out.Hosts, ServerOpsHost{Host: row.Host, SampledAt: row.TS, Alerts: []string{}})
		}
		host := &out.Hosts[len(out.Hosts)-1]
		switch row.Metric {
		case serverops.MetricUp:
			if row.State != nil {
				host.Up = new(*row.State == "up")
			}
		case serverops.MetricCPU:
			host.CPU = row.Value
		case serverops.MetricMemory:
			host.Memory = row.Value
		case serverops.MetricDisk:
			host.Disk = row.Value
		case serverops.MetricFailedUnits:
			host.FailedUnits = row.Value
		case serverops.MetricAlerts:
			if row.Detail != nil {
				var detail struct {
					Alerts []string `json:"alerts"`
				}
				if err := json.Unmarshal([]byte(*row.Detail), &detail); err == nil && detail.Alerts != nil {
					host.Alerts = detail.Alerts
				}
			}
		}
	}
	pages, err := kb.LiveOpsPages(ctx, s.db.Read, s.clock(), overviewFindings)
	if err != nil {
		return application.Read[ServerOpsPage]{}, err
	}
	for _, page := range pages {
		out.Findings = append(out.Findings, finding(page))
	}
	link, err := grafanatools.Link(ctx, s.db.Read)
	if err != nil {
		return application.Read[ServerOpsPage]{}, err
	}
	if link != "" {
		out.GrafanaURL = &link
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return application.Read[ServerOpsPage]{}, err
	}
	return application.Read[ServerOpsPage]{Revision: application.Revision(fmt.Sprintf("%x", sha256.Sum256(raw))), Data: out}, nil
}

func finding(page kb.Record) ServerOpsFinding {
	out := ServerOpsFinding{ID: page.ID, Title: page.Title, Body: page.Body, Severity: "info", UpdatedAt: page.UpdatedAt, ExpiresAt: page.ExpiresAt}
	if page.Origin != nil {
		out.Fact = strings.TrimPrefix(*page.Origin, kb.OpsOrigin(""))
	}
	head, body, found := strings.Cut(page.Body, "\n\n")
	if severity, fact, ok := strings.Cut(head, " · "); found && ok && fact == out.Fact && (severity == "info" || severity == "warning" || severity == "critical") {
		out.Severity, out.Body = severity, body
	}
	return out
}
