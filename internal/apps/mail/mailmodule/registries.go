package mailmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
	"github.com/jeiang/atrium/internal/apps/mail/drafts"
	mailsync "github.com/jeiang/atrium/internal/apps/mail/sync"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ops"
	proposalsapp "github.com/jeiang/atrium/internal/proposals/application"
)

func Queues() []ops.Queue {
	return []ops.Queue{{Name: "drafts", Count: drafts.CountDraft}}
}

func Metrics() []ops.Metric {
	return []ops.Metric{{Name: "atrium_mail_sync_last_success_timestamp_seconds", Help: "Finish time of the last successful mail sync, by account.", Type: "gauge",
		Collect: func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
			accounts, err := mailsync.AccountSyncTimes(ctx, q)
			if err != nil {
				return nil, err
			}
			out := make([]ops.Sample, 0, len(accounts))
			for _, account := range accounts {
				out = append(out, ops.Sample{Labels: [][2]string{{"account", account.Name}}, Value: account.LastSyncAt})
			}
			return out, nil
		}}}
}

func ProposalScan() *proposalsapp.Scan {
	return &proposalsapp.Scan{Uses: []application.Name{mailapp.ScanDeletionsName}, Run: func(ctx context.Context, d *db.DB, cfg config.Config, now func() float64) ([]proposalsapp.ScanResult, bool, error) {
		scanner := mailapp.New(d, cfg, config.OS, now)
		if !scanner.ScansEnabled(ctx) {
			return nil, false, nil
		}
		out, err := scanner.ManualScans(ctx, now())
		if err != nil {
			return nil, true, err
		}
		results := make([]proposalsapp.ScanResult, 0, len(mailapp.ScanKinds))
		for _, s := range mailapp.ScanKinds {
			results = append(results, proposalsapp.ScanResult{Key: s.Key, Value: out[s.Key]})
		}
		return results, true, nil
	}}
}
