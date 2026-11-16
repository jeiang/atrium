package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/mail/scans"
	"github.com/jeiang/atrium/internal/mail/scanutil"
)

const ScanDeletionsName application.Name = "mail.scan-deletions"

type ScanInput struct{ At float64 }

func DescribeScanDeletions() application.Definition {
	return application.Describe[ScanInput, scanutil.Result](ScanDeletionsName, application.Command, []application.Authority{application.Operator, application.System}, application.Conditional, false)
}

func (s *Service) scan(ctx context.Context, key string, in ScanInput) (application.Change[scanutil.Result], error) {
	at := in.At
	if at == 0 {
		at = s.now()
	}
	for _, sc := range scans.All {
		if sc.Key == key {
			result, err := sc.Run(ctx, s.db, at)
			return application.Change[scanutil.Result]{Data: result}, err
		}
	}
	return application.Change[scanutil.Result]{}, &application.Error{Code: application.NotFound, Message: "unknown scan " + key}
}

func (s *Service) ScanDeletions(ctx context.Context, in ScanInput) (application.Change[scanutil.Result], error) {
	return s.scan(ctx, "deletions", in)
}

func (s *Service) Scans(at float64) scans.Runner {
	return func(ctx context.Context, key string) (scanutil.Result, error) {
		var done application.Change[scanutil.Result]
		var err error
		switch key {
		case "deletions":
			done, err = s.ScanDeletions(ctx, ScanInput{At: at})
		default:
			done, err = s.scan(ctx, key, ScanInput{At: at})
		}
		return done.Data, err
	}
}
