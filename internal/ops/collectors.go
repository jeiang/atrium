package ops

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/actionlink/counts"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/openloops"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	notifycounts "github.com/jeiang/atrium/internal/notify/counts"
	"github.com/jeiang/atrium/internal/proposals"
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
		queues := []struct {
			name  string
			count func(context.Context, db.Querier) (int, error)
		}{
			{"thread_queue", journal.QueueDepth},
			{"embed_index", embed.PendingTotal},
			{"drafts", drafts.CountDraft},
			{"proposals", proposals.CountPending},
			{"open_loops", openloops.CountOpen},
		}
		out := make([]Sample, 0, len(queues))
		for _, queue := range queues {
			n, err := queue.count(ctx, q)
			if err != nil {
				return nil, err
			}
			out = append(out, Sample{Labels: [][2]string{{"queue", queue.name}}, Value: float64(n)})
		}
		return out, nil
	}))
	Register(gauge("atrium_integration_state", "Current health state of each configured integration.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		states, err := integrations.HealthStates(ctx, q)
		if err != nil {
			return nil, err
		}
		out := make([]Sample, 0, len(states))
		for _, state := range states {
			out = append(out, Sample{Labels: [][2]string{{"integration", state.Name}, {"state", state.State}}, Value: 1})
		}
		return out, nil
	}))
	Register(gauge("atrium_action_links_outstanding", "Phone action links that are unused and unexpired.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		now := float64(time.Now().UnixNano()) / 1e9
		n, err := counts.Outstanding(ctx, q, now)
		return one(float64(n)), err
	}))
	Register(gauge("atrium_action_links_minted_last_hour", "Phone action links created in the last hour.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		now := float64(time.Now().UnixNano()) / 1e9
		n, err := counts.MintedSince(ctx, q, now-3600)
		return one(float64(n)), err
	}))
	Register(gauge("atrium_action_links_used_total", "Phone action links redeemed since the database was created.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		n, err := counts.Used(ctx, q)
		return one(float64(n)), err
	}))
	Register(gauge("atrium_endpoint_probe_ok", "1 when the last probe of a chat or embed endpoint succeeded.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		probes, err := mail.Probes(ctx, q)
		if err != nil {
			return nil, err
		}
		out := make([]Sample, 0, len(probes))
		for _, probe := range probes {
			out = append(out, Sample{Labels: [][2]string{{"kind", probe.Kind}}, Value: float64(probe.OK)})
		}
		return out, nil
	}))
	Register(gauge("atrium_endpoint_probe_timestamp_seconds", "Time of the last chat or embed endpoint probe.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		probes, err := mail.Probes(ctx, q)
		if err != nil {
			return nil, err
		}
		out := make([]Sample, 0, len(probes))
		for _, probe := range probes {
			out = append(out, Sample{Labels: [][2]string{{"kind", probe.Kind}}, Value: probe.ProbedAt})
		}
		return out, nil
	}))
	Register(gauge("atrium_endpoint_probe_dimension", "Embedding dimension reported by the last embed endpoint probe.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		probes, err := mail.Probes(ctx, q)
		if err != nil {
			return nil, err
		}
		out := make([]Sample, 0, len(probes))
		for _, probe := range probes {
			out = append(out, Sample{Labels: [][2]string{{"kind", probe.Kind}}, Value: float64(probe.Dimension)})
		}
		return out, nil
	}))
	Register(gauge("atrium_notifications_undelivered", "Recorded notifications that were not delivered.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		n, err := notifycounts.Undelivered(ctx, q)
		return one(float64(n)), err
	}))
	Register(gauge("atrium_notification_push_failing_devices", "Push devices whose last three or more deliveries failed.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		n, err := notifycounts.FailingPush(ctx, q)
		return one(float64(n)), err
	}))
	Register(gauge("atrium_mail_sync_last_success_timestamp_seconds", "Finish time of the last successful mail sync, by account.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		accounts, err := mailsync.AccountSyncTimes(ctx, q)
		if err != nil {
			return nil, err
		}
		out := make([]Sample, 0, len(accounts))
		for _, account := range accounts {
			out = append(out, Sample{Labels: [][2]string{{"account", account.Name}}, Value: account.LastSyncAt})
		}
		return out, nil
	}))
}
