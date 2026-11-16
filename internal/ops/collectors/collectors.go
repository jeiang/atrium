package collectors

import (
	"context"

	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	notifycounts "github.com/jeiang/atrium/internal/notify/counts"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/proposals"
)

func gauge(name, help string, collect func(ctx context.Context, q db.Querier) ([]ops.Sample, error)) ops.Metric {
	return ops.Metric{Name: name, Help: help, Type: "gauge", Collect: collect}
}

func one(v float64) []ops.Sample { return []ops.Sample{{Value: v}} }

func Queues() []ops.Queue {
	return []ops.Queue{{Name: "embed_index", Count: embed.PendingTotal}, {Name: "proposals", Count: proposals.CountPending}}
}

func Metrics() []ops.Metric {
	var metrics []ops.Metric
	add := func(m ops.Metric) { metrics = append(metrics, m) }
	add(ops.Metric{Name: "atrium_db_exclusive_waits_total", Help: "Exclusive database lock acquisitions that had to wait, in this process.", Type: "counter",
		Collect: func(context.Context, db.Querier) ([]ops.Sample, error) {
			return one(float64(db.ExclusiveWaits.Load())), nil
		}})
	add(ops.Metric{Name: "atrium_db_exclusive_timeouts_total", Help: "Exclusive database lock waits that timed out, in this process.", Type: "counter",
		Collect: func(context.Context, db.Querier) ([]ops.Sample, error) {
			return one(float64(db.ExclusiveTimeouts.Load())), nil
		}})
	add(gauge("atrium_integration_state", "Current health state of each connection.", func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
		states, err := connections.HealthStates(ctx, q)
		if err != nil {
			return nil, err
		}
		out := make([]ops.Sample, 0, len(states))
		for _, state := range states {
			out = append(out, ops.Sample{Labels: [][2]string{{"integration", state.ID}, {"state", string(state.State)}}, Value: 1})
		}
		return out, nil
	}))
	add(gauge("atrium_notifications_undelivered", "Recorded notifications that were not delivered.", func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
		n, err := notifycounts.Undelivered(ctx, q)
		return one(float64(n)), err
	}))
	add(gauge("atrium_notification_push_failing_devices", "Push devices whose last three or more deliveries failed.", func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
		n, err := notifycounts.FailingPush(ctx, q)
		return one(float64(n)), err
	}))
	return metrics
}
