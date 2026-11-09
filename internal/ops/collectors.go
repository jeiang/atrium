package ops

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

func gauge(name, help string, collect func(ctx context.Context, q db.Querier) ([]Sample, error)) Metric {
	return Metric{Name: name, Help: help, Type: "gauge", Collect: collect}
}

func one(v float64) []Sample { return []Sample{{Value: v}} }

func init() {
	Register(Metric{Name: "atrium_db_exclusive_waits_total", Help: "Exclusive database lock acquisitions that had to wait, in this process.", Type: "counter",
		Collect: func(context.Context, db.Querier) ([]Sample, error) {
			return one(float64(db.ExclusiveWaits.Load())), nil
		}})
	Register(Metric{Name: "atrium_db_exclusive_timeouts_total", Help: "Exclusive database lock waits that timed out, in this process.", Type: "counter",
		Collect: func(context.Context, db.Querier) ([]Sample, error) {
			return one(float64(db.ExclusiveTimeouts.Load())), nil
		}})
	Register(gauge("atrium_queue_depth", "Open items in each attention queue.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		queues := []struct{ name, query string }{
			{"thread_queue", "SELECT COUNT(*) FROM thread_queue WHERE consumed_at IS NULL"},
			{"embed_index", "SELECT COALESCE(SUM(total - done), 0) FROM embed_index"},
			{"drafts", "SELECT COUNT(*) FROM drafts WHERE status = 'draft'"},
			{"proposals", "SELECT COUNT(*) FROM proposals WHERE status = 'pending'"},
			{"open_loops", "SELECT COUNT(*) FROM open_loops"},
		}
		out := make([]Sample, 0, len(queues))
		for _, queue := range queues {
			var n int64
			if err := q.QueryRowContext(ctx, queue.query).Scan(&n); err != nil {
				return nil, err
			}
			out = append(out, Sample{Labels: [][2]string{{"queue", queue.name}}, Value: float64(n)})
		}
		return out, nil
	}))
	Register(gauge("atrium_integration_state", "Current health state of each configured integration.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		rows, err := q.QueryContext(ctx, "SELECT name, state FROM integration_health ORDER BY name")
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		var out []Sample
		for rows.Next() {
			var name, state string
			if err := rows.Scan(&name, &state); err != nil {
				return nil, err
			}
			out = append(out, Sample{Labels: [][2]string{{"integration", name}, {"state", state}}, Value: 1})
		}
		return out, rows.Err()
	}))
	Register(gauge("atrium_action_links_outstanding", "Phone action links that are unused and unexpired.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		var n int64
		now := float64(time.Now().UnixNano()) / 1e9
		err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM action_links WHERE used_ts IS NULL AND expires_ts > ?", now).Scan(&n)
		return one(float64(n)), err
	}))
	Register(gauge("atrium_action_links_minted_last_hour", "Phone action links created in the last hour.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		var n int64
		now := float64(time.Now().UnixNano()) / 1e9
		err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM action_links WHERE created_ts > ?", now-3600).Scan(&n)
		return one(float64(n)), err
	}))
	Register(gauge("atrium_action_links_used_total", "Phone action links redeemed since the database was created.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		var n int64
		err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM action_links WHERE used_ts IS NOT NULL").Scan(&n)
		return one(float64(n)), err
	}))
	Register(gauge("atrium_endpoint_probe_ok", "1 when the last probe of a chat or embed endpoint succeeded.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		rows, err := q.QueryContext(ctx, "SELECT kind, ok FROM endpoint_probes ORDER BY kind")
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		var out []Sample
		for rows.Next() {
			var kind string
			var ok int64
			if err := rows.Scan(&kind, &ok); err != nil {
				return nil, err
			}
			out = append(out, Sample{Labels: [][2]string{{"kind", kind}}, Value: float64(ok)})
		}
		return out, rows.Err()
	}))
	Register(gauge("atrium_endpoint_probe_timestamp_seconds", "Time of the last chat or embed endpoint probe.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		rows, err := q.QueryContext(ctx, "SELECT kind, probed_at FROM endpoint_probes ORDER BY kind")
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		var out []Sample
		for rows.Next() {
			var kind string
			var at float64
			if err := rows.Scan(&kind, &at); err != nil {
				return nil, err
			}
			out = append(out, Sample{Labels: [][2]string{{"kind", kind}}, Value: at})
		}
		return out, rows.Err()
	}))
	Register(gauge("atrium_endpoint_probe_dimension", "Embedding dimension reported by the last embed endpoint probe.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		rows, err := q.QueryContext(ctx, "SELECT kind, COALESCE(dimension, 0) FROM endpoint_probes ORDER BY kind")
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		var out []Sample
		for rows.Next() {
			var kind string
			var dimension int64
			if err := rows.Scan(&kind, &dimension); err != nil {
				return nil, err
			}
			out = append(out, Sample{Labels: [][2]string{{"kind", kind}}, Value: float64(dimension)})
		}
		return out, rows.Err()
	}))
	Register(gauge("atrium_notifications_undelivered", "Recorded notifications that were not delivered.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		var n int64
		err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications WHERE delivered = 0").Scan(&n)
		return one(float64(n)), err
	}))
	Register(gauge("atrium_notification_push_failing_devices", "Push devices whose last three or more deliveries failed.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		var n int64
		err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM push_subscriptions WHERE failure_count >= 3").Scan(&n)
		return one(float64(n)), err
	}))
	Register(gauge("atrium_mail_sync_last_success_timestamp_seconds", "Finish time of the last successful mail sync, by account.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		rows, err := q.QueryContext(ctx, "SELECT name, last_sync_at FROM accounts WHERE last_sync_at IS NOT NULL ORDER BY name")
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		var out []Sample
		for rows.Next() {
			var name string
			var at float64
			if err := rows.Scan(&name, &at); err != nil {
				return nil, err
			}
			out = append(out, Sample{Labels: [][2]string{{"account", name}}, Value: at})
		}
		return out, rows.Err()
	}))
}
