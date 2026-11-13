package application

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/progress"
)

const StreamRetentionName application.Name = "assistant.stream-retention"

type StreamRetentionInput struct{}

type StreamsPruned struct {
	Streams int64 `json:"streams"`
}

func DescribeStreamRetention() application.Definition {
	return application.Describe[StreamRetentionInput, StreamsPruned](StreamRetentionName, application.Command,
		[]application.Authority{application.System}, application.NoEffect, false)
}

func (s *Service) StreamRetention(ctx context.Context, _ StreamRetentionInput) (application.Change[StreamsPruned], error) {
	var out StreamsPruned
	var auditID int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if out.Streams, err = progress.Prune(ctx, tx, s.now()); err != nil {
			return err
		}
		auditID, err = s.audit(ctx, tx, StreamRetentionName, "", state.OK)
		return err
	})
	if err != nil {
		return application.Change[StreamsPruned]{}, s.reject(ctx, StreamRetentionName, "", err)
	}
	return application.Change[StreamsPruned]{Data: out, AuditID: auditID}, nil
}
