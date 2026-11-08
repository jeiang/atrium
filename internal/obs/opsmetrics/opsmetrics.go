package opsmetrics

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/obs"
	"github.com/jeiang/atrium/internal/ops"
)

func init() {
	for _, metric := range obs.Metrics() {
		metric := metric
		ops.Register(ops.Metric{
			Name: metric.Name, Help: metric.Help, Type: metric.Type,
			Collect: func(context.Context, db.Querier) ([]ops.Sample, error) {
				samples := metric.Snapshot()
				out := make([]ops.Sample, len(samples))
				for i, sample := range samples {
					out[i] = ops.Sample{Name: sample.Name, Labels: sample.Labels, Value: sample.Value}
				}
				return out, nil
			},
		})
	}
}
