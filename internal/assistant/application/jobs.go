package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/digest"
	"github.com/jeiang/atrium/internal/assistant/dreaming"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	attentionapp "github.com/jeiang/atrium/internal/attention/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

const (
	DreamName       application.Name = "assistant.dream"
	DigestName      application.Name = "assistant.digest"
	ScheduleRunName application.Name = "assistant.schedule.run"
)

type DreamInput struct {
	Claim scheduler.Claim
}

type DreamResult struct {
	Runs []int64 `json:"runs"`
}

type DigestInput struct {
	JobRunID int64
}

type DigestResult struct {
	Posted bool `json:"posted"`
}

type ScheduleRunInput struct {
	Claim    scheduler.Claim
	Text     string
	Operator bool
}

func DescribeJobs() []application.Definition {
	system := []application.Authority{application.System}
	return []application.Definition{
		application.Describe[DreamInput, DreamResult](DreamName, application.Command, system, application.External, true),
		application.Describe[DigestInput, DigestResult](DigestName, application.Command, system, application.NoEffect, true),
		application.Describe[ScheduleRunInput, Started](ScheduleRunName, application.Command, system, application.External, true),
	}
}

func jobOutcome(err error, empty bool) state.Outcome {
	var exceeded inference.BudgetExceeded
	var missing config.NotConfigured
	var off *module.OwnerDisabled
	switch {
	case err == nil && empty:
		return state.Noop
	case err == nil:
		return state.OK
	case errors.As(err, &exceeded), errors.As(err, &missing), errors.As(err, &off):
		return state.Refused
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return state.Canceled
	}
	return state.Failed
}

func (s *Service) settle(ctx context.Context, name application.Name, target string, err error, empty bool) (int64, error) {
	id, aerr := s.audit(context.WithoutCancel(ctx), nil, name, target, jobOutcome(err, empty))
	return id, errors.Join(err, aerr)
}

func (s *Service) Dream(ctx context.Context, in DreamInput) (application.Change[DreamResult], error) {
	runs, err := dreaming.Run(ctx, s.runs(), s.now, in.Claim)
	id, err := s.settle(ctx, DreamName, "", err, len(runs) == 0)
	return application.Change[DreamResult]{Data: DreamResult{Runs: runs}, AuditID: id}, err
}

func (s *Service) Digest(ctx context.Context, in DigestInput) (application.Change[DigestResult], error) {
	read, err := attentionapp.New(s.db, s.now).Read(ctx, attentionapp.ReadInput{})
	posted := false
	if err == nil {
		posted, err = digest.Run(ctx, s.runs(), s.now, in.JobRunID, read.Data)
	}
	id, err := s.settle(ctx, DigestName, "", err, !posted)
	return application.Change[DigestResult]{Data: DigestResult{Posted: posted}, AuditID: id}, err
}

func (s *Service) ScheduleRun(ctx context.Context, in ScheduleRunInput) (application.Change[Started], error) {
	job := in.Claim.Job
	start := StartInput{Kind: threads.Cron, Title: &job.Name, Claim: &in.Claim, Text: in.Text, Operator: in.Operator, Wait: true}
	if !in.Operator {
		marks, err := threads.MarksOf(ctx, s.db.Read, []string{fmt.Sprintf("schedule:%d", job.ID)})
		if err != nil {
			return application.Change[Started]{}, err
		}
		if marks != 0 {
			at := s.now()
			start.Marks = threads.Marks{At: &at, Set: marks}
		}
	}
	var started Started
	runID, err := s.threadStart(ctx, start, &started)
	started.RunID = runID
	id, err := s.settle(ctx, ScheduleRunName, target("thread", started.ThreadID), err, false)
	return application.Change[Started]{Data: started, AuditID: id}, err
}

func ScheduledStart(ctx context.Context, svc *service.Service, run scheduler.Run, text string, operator bool) (int64, int64, error) {
	change, err := New(svc.DB, svc.Env, run.Now).WithRuns(func() *service.Service { return svc }).
		ScheduleRun(ctx, ScheduleRunInput{Claim: run.Claim, Text: text, Operator: operator})
	return change.Data.ThreadID, change.Data.RunID, err
}
