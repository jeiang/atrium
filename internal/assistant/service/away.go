package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/tool"
)

func questionOccurrence(callID int64) string { return fmt.Sprintf("ask:%d", callID) }

func (s *Service) away(ctx context.Context, run *journal.Run, cause error, watched bool) {
	if watched || run.Kind != string(tool.Chat) || !notify.Enabled(ctx) {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if !audit.HasActor(ctx) {
		ctx = audit.With(ctx, "service:assistant", audit.Job)
	}
	var m notify.Message
	switch run.State {
	case journal.Parked:
		q, err := PendingQuestion(ctx, s.DB.Read, run.ThreadID)
		if err != nil || q == nil {
			s.warnAway(run, err)
			return
		}
		m = notify.Message{
			Channel: notify.Assistant, Title: "atrium: question waiting",
			Body:       "The assistant is waiting for your answer in " + s.threadName(ctx, run.ThreadID) + ".",
			Occurrence: questionOccurrence(q.CallID),
		}
	case journal.Failed:
		reason, ok := failureClass(cause)
		if !ok {
			return
		}
		m = notify.Message{
			Channel: notify.System, Title: "atrium: run failed",
			Body:       "The run in " + s.threadName(ctx, run.ThreadID) + " stopped: " + reason + ".",
			RunID:      run.ID,
			Occurrence: fmt.Sprintf("run-failed:%d", run.ID),
		}
	default:
		return
	}
	m.ThreadID, m.Now = run.ThreadID, s.now
	_, err := notify.Send(ctx, s.DB, m)
	s.warnAway(run, err)
}

func (s *Service) warnAway(run *journal.Run, err error) {
	if err != nil {
		log.Warn("run push failed", logx.RunID, run.ID, logx.Err, err.Error())
	}
}

func (s *Service) threadName(ctx context.Context, threadID int64) string {
	if t, err := threads.Get(ctx, s.DB.Read, threadID); err == nil && t != nil && t.Title != nil && *t.Title != "" {
		return fmt.Sprintf("%q", *t.Title)
	}
	return fmt.Sprintf("thread %d", threadID)
}

func failureClass(cause error) (string, bool) {
	var api *anthropic.APIError
	var nc config.NotConfigured
	switch {
	case cause == nil || errors.Is(cause, progress.ErrStopped):
		return "", false
	case errors.As(cause, new(inference.BudgetExceeded)):
		return "a spending limit was reached", true
	case errors.As(cause, new(loop.RunLimit)):
		return "it reached a run limit", true
	case errors.As(cause, &nc):
		return "the model is not set up", true
	case errors.As(cause, &api):
		return "the model service returned an error", true
	default:
		return "an internal error", true
	}
}
