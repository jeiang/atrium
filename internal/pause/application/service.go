package application

import (
	"context"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/pause"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	ReadName   application.Name = "automation.pause-status"
	PauseName  application.Name = "automation.pause"
	ResumeName application.Name = "automation.resume"
)

type PauseInput struct {
	Reason *string `json:"reason"`
}

type Changed struct {
	State       pause.State
	AuditID     int64
	NoticeError error
}

var operator = []application.Authority{application.Operator}

func DescribeRead() application.Definition {
	return application.Describe[struct{}, pause.State](ReadName, application.Query, operator, application.NoEffect, false)
}

func DescribePause() application.Definition {
	return application.Describe[PauseInput, pause.State](PauseName, application.Command, operator, application.NoEffect, true)
}

func DescribeResume() application.Definition {
	return application.Describe[struct{}, pause.State](ResumeName, application.Command, operator, application.NoEffect, true)
}

type Service struct {
	db    *db.DB
	cfg   config.Config
	env   config.Env
	state *state.Store
}

func New(d *db.DB, cfg config.Config, env config.Env) *Service {
	return &Service{db: d, cfg: cfg, env: env, state: state.New(d)}
}

func (s *Service) Read(ctx context.Context, _ struct{}) (application.Read[pause.State], error) {
	out, err := pause.Read(s.cfg.PausePath())
	if err != nil {
		return application.Read[pause.State]{}, err
	}
	counters, err := s.state.Read(ctx, nil, []string{state.Automation})
	if err != nil {
		return application.Read[pause.State]{}, err
	}
	return application.Read[pause.State]{Revision: application.Revision(state.Revision(string(ReadName), counters, out, "")), Data: out}, nil
}

func (s *Service) Pause(ctx context.Context, in PauseInput) (application.Change[pause.State], error) {
	reason := ""
	if in.Reason != nil {
		reason = strings.TrimSpace(*in.Reason)
	}
	changed, err := s.Set(ctx, true, reason)
	return application.Change[pause.State]{Data: changed.State, AuditID: changed.AuditID}, err
}

func (s *Service) Resume(ctx context.Context, _ struct{}) (application.Change[pause.State], error) {
	changed, err := s.Set(ctx, false, "")
	return application.Change[pause.State]{Data: changed.State, AuditID: changed.AuditID}, err
}

func (s *Service) Set(ctx context.Context, paused bool, reason string) (Changed, error) {
	name := ResumeName
	if paused {
		name = PauseName
	}
	out, err := s.change(ctx, paused, reason)
	outcome := state.OK
	if err != nil {
		outcome = state.Failed
	}
	id, auditErr := s.state.Audit(context.WithoutCancel(ctx), nil, state.Entry{Operation: string(name), Outcome: outcome})
	if auditErr != nil {
		logx.Logger("pause").Warn("pause changed but audit failed", "operation", string(name), logx.Err, auditErr.Error())
	}
	out.AuditID = id
	if err != nil {
		return out, err
	}
	if set := module.SetFrom(ctx); set != nil && set.Current() == nil {
		if rerr := set.Refresh(ctx, s.db.Read); rerr != nil {
			logx.Logger("pause").Warn("pause changed but module state could not be read for its notice", logx.Err, rerr.Error())
		}
	}
	out.NoticeError = notify.PauseChanged(ctx, s.db, s.env, paused)
	return out, nil
}

func (s *Service) change(ctx context.Context, paused bool, reason string) (Changed, error) {
	path := s.cfg.PausePath()
	var err error
	if paused {
		err = pause.Set(path, pause.Record{By: audit.Actor(ctx), Source: string(audit.SourceOf(ctx)), Reason: reason})
	} else {
		err = pause.Clear(path)
	}
	if err != nil {
		return Changed{}, err
	}
	var out Changed
	if out.State, err = pause.Read(path); err != nil {
		return Changed{}, err
	}
	if terr := s.state.Touch(context.WithoutCancel(ctx), state.Automation); terr != nil {
		logx.Logger("pause").Warn("pause changed but its event topic was not bumped", logx.Err, terr.Error())
	}
	return out, nil
}
