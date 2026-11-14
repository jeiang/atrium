package application

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/kb/export"
)

const ExportName application.Name = "knowledge.export"

type ExportInput struct{}

func DescribeExport() application.Definition {
	return application.Describe[ExportInput, export.Result](ExportName, application.Command, []application.Authority{application.System}, application.External, true)
}

func (s *Service) WithEnv(env config.Env) *Service {
	c := *s
	c.env = env
	return &c
}

func (s *Service) Export(ctx context.Context, _ ExportInput) (application.Change[export.Result], error) {
	res, err := export.Run(ctx, s.db, s.env, s.now())
	outcome := state.OK
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			outcome = state.Canceled
		case res != export.Result{}:
			outcome = state.Partial
		default:
			outcome = state.Failed
		}
	}
	auditN, aerr := s.audit(context.WithoutCancel(ctx), nil, ExportName, 0, outcome, 0)
	return application.Change[export.Result]{Data: res, AuditID: auditN}, errors.Join(err, aerr)
}
