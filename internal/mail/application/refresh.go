package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/module"
)

const (
	RefreshLoopsName   application.Name = "mail.refresh-loops"
	RefreshWatchesName application.Name = "mail.refresh-watches"
)

type RefreshResult struct{ Result any }

func DescribeRefreshLoops() application.Definition {
	return application.Describe[struct{}, RefreshResult](RefreshLoopsName, application.Command, []application.Authority{application.Operator}, application.Conditional, false)
}
func DescribeRefreshWatches() application.Definition {
	return application.Describe[struct{}, RefreshResult](RefreshWatchesName, application.Command, []application.Authority{application.Operator}, application.Conditional, false)
}

func (s *Service) WithClassifier(c *classifier.Service) *Service { s.classifier = c; return s }
func (s *Service) WithClock(now func() float64) *Service         { s.now = now; return s }

func (s *Service) RefreshLoops(ctx context.Context, _ struct{}) (application.Change[RefreshResult], error) {
	out, err := module.RefreshCommunications(ctx, s.db, s.now(), module.Refresh{Env: s.env, Classifier: s.classifier, Clock: s.now})
	if err != nil {
		return application.Change[RefreshResult]{}, err
	}
	return application.Change[RefreshResult]{Data: RefreshResult{Result: out}}, nil
}

func (s *Service) RefreshWatches(ctx context.Context, _ struct{}) (application.Change[RefreshResult], error) {
	out, err := watches.Refresh(ctx, s.db, s.now(), watches.Options{Classifier: s.classifier, Clock: s.now, Env: s.env})
	if err != nil {
		return application.Change[RefreshResult]{}, err
	}
	return application.Change[RefreshResult]{Data: RefreshResult{Result: out}}, nil
}
