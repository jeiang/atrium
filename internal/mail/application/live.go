package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/notify"
)

const (
	PauseName       application.Name = "mail.pause"
	ResumeName      application.Name = "mail.resume"
	LiveStatusName  application.Name = "mail.live-status"
	LiveActionsName application.Name = "mail.live-actions"
)

type PauseInput struct{}
type PauseResult struct {
	Paused      bool  `json:"paused"`
	NoticeError error `json:"-"`
}
type LiveActionsInput struct {
	Limit  int
	Kind   string
	Offset int
}

func (s *Service) WithLive(l *live.Live) *Service { s.live = l; return s }
func (s *Service) liveService() *live.Live {
	if s.live != nil {
		return s.live
	}
	return &live.Live{DB: s.db, Cfg: s.cfg}
}
func DescribePause() application.Definition {
	return application.Describe[PauseInput, PauseResult](PauseName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeResume() application.Definition {
	return application.Describe[PauseInput, PauseResult](ResumeName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeLiveStatus() application.Definition {
	return application.Describe[struct{}, live.Status](LiveStatusName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func DescribeLiveActions() application.Definition {
	return application.Describe[LiveActionsInput, []live.Action](LiveActionsName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func (s *Service) Pause(ctx context.Context, in PauseInput) (application.Change[PauseResult], error) {
	return s.setPaused(ctx, true)
}
func (s *Service) Resume(ctx context.Context, in PauseInput) (application.Change[PauseResult], error) {
	return s.setPaused(ctx, false)
}
func (s *Service) setPaused(ctx context.Context, paused bool) (application.Change[PauseResult], error) {
	l := s.liveService()
	name := ResumeName
	if paused {
		name = PauseName
	}
	err := l.SetPaused(paused)
	var out PauseResult
	if err == nil {
		out.NoticeError = notify.PauseChanged(ctx, s.db, s.env, paused)
		out.Paused = l.Paused()
	}
	id, err := s.settle(ctx, name, "", err, state.OK)
	return application.Change[PauseResult]{Data: out, AuditID: id}, err
}
func (s *Service) LiveStatus(ctx context.Context, in struct{}) (application.Read[live.Status], error) {
	out, err := s.liveService().Status(ctx)
	if err != nil {
		return application.Read[live.Status]{}, err
	}
	rev, err := s.snapshot(ctx, LiveStatusName, out, func(db.Querier) error { return nil })
	return application.Read[live.Status]{Revision: rev, Data: out}, err
}
func (s *Service) LiveActions(ctx context.Context, in LiveActionsInput) (application.Read[[]live.Action], error) {
	out, err := s.liveService().List(ctx, "", in.Kind, in.Limit, in.Offset)
	if err != nil {
		return application.Read[[]live.Action]{}, err
	}
	rev, err := s.snapshot(ctx, LiveActionsName, []any{in, out}, func(db.Querier) error { return nil })
	return application.Read[[]live.Action]{Revision: rev, Data: out}, err
}
