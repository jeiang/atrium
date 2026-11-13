package application

import (
	"context"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
)

const QuestionAskName application.Name = "assistant.question.ask"

type QuestionAskInput struct {
	ThreadID int64
	Question string
}

type QuestionAsked struct {
	Question string
}

func DescribeQuestionAsk() application.Definition {
	return application.Describe[QuestionAskInput, QuestionAsked](QuestionAskName, application.Command, agentWriters, application.NoEffect, true)
}

func (s *Service) QuestionAsk(ctx context.Context, in QuestionAskInput) (application.Change[QuestionAsked], error) {
	where := target("thread", in.ThreadID)
	text := strings.TrimSpace(in.Question)
	if text == "" {
		return application.Change[QuestionAsked]{}, s.reject(ctx, QuestionAskName, where,
			&application.Error{Code: application.Invalid, Message: "question is required"})
	}
	auditID, err := s.audit(ctx, nil, QuestionAskName, where, state.OK)
	if err != nil {
		return application.Change[QuestionAsked]{}, err
	}
	return application.Change[QuestionAsked]{Data: QuestionAsked{Question: text}, AuditID: auditID}, nil
}
