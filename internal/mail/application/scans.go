package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/mail/scans"
	"github.com/jeiang/atrium/internal/mail/scanutil"
)

const (
	ScanRebalanceName   application.Name = "mail.scan-rebalance"
	ScanDeletionsName   application.Name = "mail.scan-deletions"
	ScanGmailLabelsName application.Name = "mail.scan-gmail-labels"
)

type ScanInput struct{ At float64 }

func describeScan(name application.Name) application.Definition {
	return application.Describe[ScanInput, scanutil.Result](name, application.Command, []application.Authority{application.Operator, application.System}, application.Conditional, false)
}
func DescribeScanRebalance() application.Definition   { return describeScan(ScanRebalanceName) }
func DescribeScanDeletions() application.Definition   { return describeScan(ScanDeletionsName) }
func DescribeScanGmailLabels() application.Definition { return describeScan(ScanGmailLabelsName) }

func (s *Service) WithNamer(n rebalance.Namer) *Service { s.namer = n; return s }

func (s *Service) scan(ctx context.Context, key string, in ScanInput) (application.Change[scanutil.Result], error) {
	at := in.At
	if at == 0 {
		at = s.now()
	}
	for _, sc := range scans.All {
		if sc.Key == key {
			result, err := sc.Run(ctx, s.db, at, s.namer)
			return application.Change[scanutil.Result]{Data: result}, err
		}
	}
	return application.Change[scanutil.Result]{}, &application.Error{Code: application.NotFound, Message: "unknown scan " + key}
}

func (s *Service) ScanRebalance(ctx context.Context, in ScanInput) (application.Change[scanutil.Result], error) {
	return s.scan(ctx, "rebalance", in)
}

func (s *Service) ScanDeletions(ctx context.Context, in ScanInput) (application.Change[scanutil.Result], error) {
	return s.scan(ctx, "deletions", in)
}

func (s *Service) ScanGmailLabels(ctx context.Context, in ScanInput) (application.Change[scanutil.Result], error) {
	return s.scan(ctx, "gmail-labels", in)
}

func (s *Service) Scans(at float64) scans.Runner {
	return func(ctx context.Context, key string) (scanutil.Result, error) {
		var done application.Change[scanutil.Result]
		var err error
		switch key {
		case "rebalance":
			done, err = s.ScanRebalance(ctx, ScanInput{At: at})
		case "deletions":
			done, err = s.ScanDeletions(ctx, ScanInput{At: at})
		case "gmail-labels":
			done, err = s.ScanGmailLabels(ctx, ScanInput{At: at})
		default:
			done, err = s.scan(ctx, key, ScanInput{At: at})
		}
		return done.Data, err
	}
}
