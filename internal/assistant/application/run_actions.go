package application

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
)

const (
	RunStopName   application.Name = "assistant.run.stop"
	RunResumeName application.Name = "assistant.run.resume"
	RunAnswerName application.Name = "assistant.run.answer"
)

type NoActiveRun int64

func (e NoActiveRun) Error() string { return "No run is running." }

type NoQuestion int64

func (e NoQuestion) Error() string { return "No question is waiting for an answer." }

type NotAnswered struct{ Err error }

func (e NotAnswered) Error() string { return e.Err.Error() }

func (e NotAnswered) Unwrap() error { return e.Err }

type ResumeInput struct {
	ThreadID int64
	RunID    int64
}

type AnswerInput struct {
	ThreadID int64
	Text     string
}

func DescribeRunStop() application.Definition {
	return describeThreadCommand[ThreadInput](RunStopName, application.NoEffect)
}

func DescribeRunResume() application.Definition {
	return describeThreadCommand[ResumeInput](RunResumeName, application.External)
}

func DescribeRunAnswer() application.Definition {
	return describeThreadCommand[AnswerInput](RunAnswerName, application.External)
}

func (s *Service) RunStop(ctx context.Context, in ThreadInput) (application.Change[ThreadChanged], error) {
	var runID int64
	change, err := s.inTx(ctx, RunStopName, in.ID, func(tx *sql.Tx) error {
		thread, err := threads.Get(ctx, tx, in.ID)
		if err != nil {
			return err
		}
		if thread == nil {
			return service.UnknownThread(in.ID)
		}
		active, err := journal.ActiveRun(ctx, tx, in.ID)
		if err != nil {
			return err
		}
		if active == nil {
			return NoActiveRun(in.ID)
		}
		stopped, err := journal.RequestStop(ctx, tx, active.ID)
		if err == nil && !stopped {
			err = NoActiveRun(in.ID)
		}
		runID = active.ID
		return err
	})
	change.Data.RunID = runID
	return change, err
}

func (s *Service) RunResume(ctx context.Context, in ResumeInput) (application.Change[ThreadChanged], error) {
	return s.act(ctx, RunResumeName, in.ThreadID, func() (ThreadChanged, error) {
		run, err := journal.GetRun(ctx, s.db.Read, in.RunID)
		if err != nil {
			return ThreadChanged{}, err
		}
		if run == nil || run.ThreadID != in.ThreadID {
			return ThreadChanged{}, notFound("unknown run")
		}
		ctx, err := s.admit(ctx, true)
		if err != nil {
			return ThreadChanged{}, err
		}
		return ThreadChanged(in), s.runs().StartResume(ctx, in.RunID)
	})
}

func (s *Service) RunAnswer(ctx context.Context, in AnswerInput) (application.Change[ThreadChanged], error) {
	return s.act(ctx, RunAnswerName, in.ThreadID, func() (ThreadChanged, error) {
		question, err := service.PendingQuestion(ctx, s.db.Read, in.ThreadID)
		if err != nil {
			return ThreadChanged{}, err
		}
		if question == nil {
			return ThreadChanged{}, NoQuestion(in.ThreadID)
		}
		ctx, err := s.admit(ctx, true)
		if err != nil {
			return ThreadChanged{}, err
		}
		if err := s.runs().Answer(ctx, in.ThreadID, in.Text); err != nil {
			if errors.Is(err, service.ErrAnswerRequired) || errors.Is(err, service.ErrNoPendingQuestion) {
				return ThreadChanged{}, NotAnswered{Err: err}
			}
			return ThreadChanged{}, err
		}
		return ThreadChanged{ThreadID: in.ThreadID, RunID: question.RunID}, nil
	})
}
