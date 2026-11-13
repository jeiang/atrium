package application

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/kb"
)

const ImportName application.Name = "knowledge.import"

type ImportInput struct {
	DryRun bool
}

func DescribeImport() application.Definition {
	return describeOperator[ImportInput, kb.ImportStats](ImportName, application.NoEffect)
}

func (s *Service) Import(ctx context.Context, in ImportInput) (application.Change[kb.ImportStats], error) {
	var auditN int64
	stats, err := kb.Import(ctx, s.db, in.DryRun, s.now(), func(ctx context.Context, tx *sql.Tx, stats kb.ImportStats) error {
		outcome := state.OK
		switch {
		case in.DryRun:
			outcome = state.DryRun
		case len(stats.FailedItems) > 0:
			outcome = state.Partial
		}
		var err error
		auditN, err = s.audit(ctx, tx, ImportName, 0, outcome, 0)
		return err
	})
	if err != nil {
		return application.Change[kb.ImportStats]{Data: stats}, err
	}
	return application.Change[kb.ImportStats]{Data: stats, AuditID: auditN}, nil
}
