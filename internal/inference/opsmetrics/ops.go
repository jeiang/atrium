package opsmetrics

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/ops"
)

func opsStatus(ctx context.Context, q db.Querier) (inference.Status, error) {
	loc, err := config.From(ctx).Location(config.EnvFrom(ctx))
	if err != nil {
		return inference.Status{}, err
	}
	return inference.StatusAt(ctx, q, float64(time.Now().UnixNano())/1e9, loc)
}

func gauge(name, help string, value func(inference.Status) float64) ops.Metric {
	return ops.Metric{Name: name, Help: help, Type: "gauge", Collect: func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
		st, err := opsStatus(ctx, q)
		return []ops.Sample{{Value: value(st)}}, err
	}}
}

func init() {
	ops.Register(gauge("atrium_budget_monthly_usd", "Monthly assistant budget in USD.", func(st inference.Status) float64 { return st.BudgetUSD }))
	ops.Register(gauge("atrium_budget_spent_usd", "Assistant spend this calendar month in USD.", func(st inference.Status) float64 { return st.SpentUSD }))
	ops.Register(gauge("atrium_budget_spent_ratio", "Share of the monthly budget spent.", func(st inference.Status) float64 { return st.Share }))
}
