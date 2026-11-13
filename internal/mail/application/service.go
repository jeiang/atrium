package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/mail/run"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
)

type Service struct {
	applier     *applier.Applier
	pass        *run.Pass
	classifiers run.Classifier
	workContext context.Context
	classifier  *classifier.Service
	namer       rebalance.Namer
	syncer      *mailsync.Syncer
	syncAll     func(context.Context) error
	effects     *ledger.Store
	sender      *drafts.Store
	open        mail.OpenFunc
	live        *live.Live
	db          *db.DB
	state       *state.Store
	cfg         config.Config
	env         config.Env
	now         func() float64
}

func New(d *db.DB, cfg config.Config, env config.Env, now func() float64) *Service {
	return &Service{db: d, state: state.New(d), cfg: cfg, env: env, now: now}
}
func (s *Service) snapshot(ctx context.Context, name application.Name, input any, fn func(db.Querier) error) (application.Revision, error) {
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, []string{state.Mail, state.Effects, state.Review, state.Settings, state.Calendar, state.Connections})
	if err != nil {
		return "", err
	}
	if err = fn(tx); err != nil {
		return "", err
	}
	return application.Revision(state.Revision(string(name), counters, input, "operator")), nil
}

func (s *Service) Mode(ctx context.Context) (string, error) { return mail.Mode(ctx, s.db.Read) }
