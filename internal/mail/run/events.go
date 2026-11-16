package run

import (
	"context"

	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/communications"
	"github.com/jeiang/atrium/internal/mail/classify"
)

type eventJudge struct {
	classify *classify.Service
	paid     *classify.Deferral
}

func (j eventJudge) Stopped() bool { return j.paid.Stopped() }

func (j eventJudge) JudgeEvent(ctx context.Context, m communications.Message, body string) (candidates.Verdict, error) {
	hasEvent, err := j.classify.JudgeEvent(ctx, classify.MessageItem(m.Account, m.ID),
		classify.Message{Subject: m.Subject, FromName: m.From.Name, FromAddr: m.From.Addr}, body)
	if j.paid.Note(err) {
		return candidates.Skip, nil
	}
	if classify.Waiting(err) {
		return candidates.Waiting, nil
	}
	if final, unanswered := classify.Unanswered(err); unanswered {
		if final {
			return candidates.NoAnswer, nil
		}
		return candidates.Skip, nil
	}
	if err != nil {
		return candidates.Skip, err
	}
	if hasEvent {
		return candidates.Event, nil
	}
	return candidates.NoEvent, nil
}
