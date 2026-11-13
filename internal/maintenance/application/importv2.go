package application

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db/importv2"
)

const ImportV2Name application.Name = "maintenance.import-v2"

type ImportV2Input struct {
	Source       string `json:"source"`
	SourceConfig string `json:"source_config,omitempty"`
	SourceEnv    string `json:"source_env,omitempty"`
}

type ImportV2Result struct {
	Lines []string `json:"lines"`
}

func DescribeImportV2() application.Definition {
	return application.Describe[ImportV2Input, ImportV2Result](ImportV2Name, application.Command, []application.Authority{application.Bootstrap}, application.LocalUndoable, true)
}

func (s *Service) ImportV2(ctx context.Context, in ImportV2Input) (application.Change[ImportV2Result], error) {
	cfg, err := s.config()
	if err != nil {
		return application.Change[ImportV2Result]{}, err
	}
	key, err := s.key(ctx)
	if err != nil {
		return application.Change[ImportV2Result]{}, err
	}
	var auditID int64
	lines, err := importv2.Run(ctx, s.env, cfg, in.Source, in.SourceConfig, in.SourceEnv, key, s.clock(), func(tx *sql.Tx, lines int) error {
		var err error
		auditID, err = state.New(nil).Audit(ctx, tx, state.Entry{Operation: string(ImportV2Name), Outcome: state.OK, DetailsJSON: fmt.Sprintf(`{"lines":%d}`, lines)})
		return err
	})
	if err != nil {
		return application.Change[ImportV2Result]{}, err
	}
	return application.Change[ImportV2Result]{Data: ImportV2Result{Lines: lines}, AuditID: auditID}, nil
}
