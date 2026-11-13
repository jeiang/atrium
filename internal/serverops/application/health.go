package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/serverops"
)

const HealthName application.Name = "ops.health"

type HealthInput struct{}

var healthMetrics = map[string]string{
	serverops.MetricCPU:         "cpu_pct",
	serverops.MetricMemory:      "mem_pct",
	serverops.MetricDisk:        "disk_root_pct",
	serverops.MetricFailedUnits: "failed_units",
	serverops.MetricAlerts:      "alerts_firing",
}

func DescribeHealth() application.Definition {
	return application.Describe[HealthInput, map[string]any](HealthName, application.Query, agentCallers, application.NoEffect, false)
}

func (s *Service) Health(ctx context.Context, in HealthInput) (application.Read[map[string]any], error) {
	loc, err := config.From(ctx).Location(s.env)
	if err != nil {
		return application.Read[map[string]any]{}, err
	}
	now := s.clock()
	var out map[string]any
	rev, err := s.snapshot(ctx, HealthName, struct {
		Hosts  []string `json:"hosts"`
		Minute int64    `json:"minute"`
	}{serverops.HostValues(ctx).Hosts, int64(now) / 60}, func(q db.Querier) error {
		last, err := serverops.LatestSampleTS(ctx, q)
		if err != nil {
			return err
		}
		found, err := serverops.LatestSamples(ctx, q)
		if err != nil {
			return err
		}
		latest := map[string]map[string]any{}
		for _, sample := range found {
			if latest[sample.Host] == nil {
				latest[sample.Host] = map[string]any{}
			}
			if sample.Metric == serverops.MetricUp {
				if sample.State != nil {
					latest[sample.Host]["up"] = *sample.State
				}
			} else if key, ok := healthMetrics[sample.Metric]; ok {
				latest[sample.Host][key] = sample.Value
			}
		}
		hosts := []any{}
		for _, host := range serverops.HostValues(ctx).Hosts {
			item := map[string]any{"host": host}
			for _, key := range []string{"up", "cpu_pct", "mem_pct", "disk_root_pct", "failed_units", "alerts_firing"} {
				if value, ok := latest[host][key]; ok {
					item[key] = value
				}
			}
			hosts = append(hosts, item)
		}
		open, err := serverops.UnresolvedFindings(ctx, q)
		if err != nil {
			return err
		}
		findings := make([]any, 0, len(open))
		for _, f := range open {
			findings = append(findings, map[string]any{"id": f.ID, "severity": f.Severity, "fact": f.Fact, "title": f.Title, "written": f.CreatedAt})
		}
		spend, err := serverops.MonthSpend(ctx, q, now, loc)
		if err != nil {
			return err
		}
		set := assistant.Read(ctx, q)
		budget := set.Float(serverops.KeyWorkerMonthlyUSD)
		if err := set.Err(); err != nil {
			return err
		}
		out = map[string]any{
			"mode": "fixed-schema-haiku", "last_sample": last, "hosts": hosts, "findings": findings,
			"spend_usd": spend, "budget_usd": budget,
		}
		return nil
	})
	return application.Read[map[string]any]{Revision: rev, Data: out}, err
}
