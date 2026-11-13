package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
)

const UsageName application.Name = "assistant.usage"

var usageScopes = []string{state.Assistant, state.Settings}

type UsageInput struct{}

type Usage struct {
	Budget       inference.Status      `json:"budget"`
	Summary      inference.Summary     `json:"summary"`
	Memory       inference.Summary     `json:"memory"`
	Subscription inference.Summary     `json:"subscription"`
	ByKind       []inference.KindRow   `json:"by_kind"`
	ByThread     []inference.ThreadRow `json:"by_thread"`
}

func DescribeUsage() application.Definition {
	return describeRead[UsageInput, Usage](UsageName)
}

func (s *Service) Usage(ctx context.Context, in UsageInput) (application.Read[Usage], error) {
	loc, err := config.From(ctx).Location(s.env)
	if err != nil {
		return application.Read[Usage]{}, err
	}
	now := s.now()
	var out Usage
	bucket := struct {
		In    UsageInput
		Month string
	}{in, inference.MonthKey(now, loc)}
	rev, err := s.snapshot(ctx, string(UsageName), usageScopes, bucket, func(q db.Querier) error {
		var err error
		if out.Budget, err = inference.StatusAt(ctx, q, now, loc); err != nil {
			return err
		}
		if out.Summary, err = inference.MonthSummary(ctx, q, now, loc); err != nil {
			return err
		}
		if out.ByKind, err = inference.ByKind(ctx, q, now, loc); err != nil {
			return err
		}
		if out.ByThread, err = inference.ByThread(ctx, q, now, loc, inference.DefaultThreadLimit); err != nil {
			return err
		}
		ids := make([]int64, len(out.ByThread))
		for i := range out.ByThread {
			ids[i] = out.ByThread[i].ThreadID
		}
		titles, err := threads.Titles(ctx, q, ids)
		if err != nil {
			return err
		}
		kinds, err := threads.KindsOf(ctx, q, ids)
		if err != nil {
			return err
		}
		for i := range out.ByThread {
			if title, ok := titles[out.ByThread[i].ThreadID]; ok {
				out.ByThread[i].Title = &title
			}
			out.ByThread[i].Kind = string(kinds[out.ByThread[i].ThreadID])
		}
		if out.Memory, err = inference.MemorySummary(ctx, q, now, loc); err != nil {
			return err
		}
		out.Subscription, err = inference.SubscriptionSummary(ctx, q, now, loc)
		return err
	})
	return application.Read[Usage]{Revision: rev, Data: out}, err
}
