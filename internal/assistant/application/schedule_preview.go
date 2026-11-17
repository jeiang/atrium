package application

import (
	"context"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/scheduler"
)

const (
	SchedulePreviewName application.Name = "assistant.schedule.preview"
	ScheduleRunReadName application.Name = "assistant.schedule.run-read"

	schedulePreviewRuns = 5
)

type SchedulePreviewInput struct {
	Schedule string
}

type SchedulePreview struct {
	Schedule string   `json:"schedule"`
	TZ       string   `json:"tz"`
	Runs     []string `json:"runs"`
}

type ScheduleRunReadInput struct {
	JobID int64
	RunID int64
}

func DescribeSchedulePreview() application.Definition {
	return describeRead[SchedulePreviewInput, SchedulePreview](SchedulePreviewName)
}

func DescribeScheduleRunRead() application.Definition {
	return describeRead[ScheduleRunReadInput, ScheduleRun](ScheduleRunReadName)
}

func (s *Service) SchedulePreview(ctx context.Context, in SchedulePreviewInput) (application.Read[SchedulePreview], error) {
	schedule := strings.TrimSpace(in.Schedule)
	tz := config.From(ctx).TZName(s.env)
	spec, err := scheduler.Parse(schedule, tz)
	if err != nil {
		return application.Read[SchedulePreview]{}, invalid("schedule", err.Error(), err)
	}
	out := SchedulePreview{Schedule: schedule, TZ: tz, Runs: []string{}}
	after := s.now()
	for range schedulePreviewRuns {
		next, ok := spec.NextAfter(after)
		if !ok {
			break
		}
		out.Runs = append(out.Runs, *scheduleInstant(&next, spec.Loc))
		after = next
	}
	rev, err := s.snapshot(ctx, string(SchedulePreviewName), []string{state.Settings}, out, func(db.Querier) error { return nil })
	return application.Read[SchedulePreview]{Revision: rev, Data: out}, err
}

func (s *Service) ScheduleRunRead(ctx context.Context, in ScheduleRunReadInput) (application.Read[ScheduleRun], error) {
	_, zone, err := s.scheduleZone(ctx)
	if err != nil {
		return application.Read[ScheduleRun]{}, err
	}
	var out ScheduleRun
	rev, err := s.snapshot(ctx, string(ScheduleRunReadName), scheduleScopes, in, func(q db.Querier) error {
		runs, err := scheduler.ListRuns(ctx, q, in.JobID, in.RunID+1, 1)
		if err != nil {
			return err
		}
		if len(runs) == 0 || runs[0].ID != in.RunID {
			return notFound("Unknown run.")
		}
		out = scheduleRunOf(runs[0], zone)
		return nil
	})
	return application.Read[ScheduleRun]{Revision: rev, Data: out}, err
}
