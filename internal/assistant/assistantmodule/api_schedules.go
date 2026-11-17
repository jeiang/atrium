package assistantmodule

import (
	"cmp"
	"context"
	"strings"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/web"
)

type apiScheduleList struct {
	Limit  api.Limit  `query:"limit" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
}

type apiSchedules struct {
	Items      []asstapp.ScheduleJob `json:"items"`
	NextCursor *string               `json:"next_cursor"`
	TZ         string                `json:"tz"`
}

type apiScheduleID struct {
	JobID int64 `path:"job_id" api:"min=1" json:"-"`
}

type apiScheduleRuns struct {
	JobID  int64      `path:"job_id" api:"min=1" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
}

type apiSchedulePatch struct {
	JobID    int64   `path:"job_id" api:"min=1" json:"-"`
	Schedule *string `json:"schedule,omitempty"`
	Prompt   *string `json:"prompt,omitempty"`
	Name     *string `json:"name,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty"`
	IfMatch  string  `header:"If-Match" json:"-"`
}

type apiScheduleCreate struct {
	Name     string `json:"name" api:"required"`
	Prompt   string `json:"prompt" api:"required"`
	Schedule string `json:"schedule" api:"required"`
}

type apiScheduleEnabled struct {
	JobID   int64 `path:"job_id" api:"min=1" json:"-"`
	Enabled *bool `json:"enabled" api:"required"`
}

type apiScheduleChanged struct {
	ID      int64                `json:"id"`
	Enabled bool                 `json:"enabled"`
	Job     *asstapp.ScheduleJob `json:"job,omitempty"`
}

func pageAfter[T any, K cmp.Ordered](items []T, key func(T) K, cursor api.Cursor, limit api.Limit) (web.List[T], error) {
	out := web.List[T]{Items: []T{}}
	if cursor.Set() {
		var last K
		if err := cursor.Last(&last); err != nil {
			return out, err
		}
		start := len(items)
		for i, item := range items {
			if key(item) > last {
				start = i
				break
			}
		}
		items = items[start:]
	}
	if len(items) > int(limit) {
		items = items[:int(limit)]
		out.NextCursor = cursor.Next(key(items[len(items)-1]))
	}
	out.Items = append(out.Items, items...)
	return out, nil
}

func scheduleChanged(value asstapp.ScheduleChanged) apiScheduleChanged {
	return apiScheduleChanged{ID: value.ID, Enabled: value.Enabled, Job: value.Job}
}

func scheduleAPIBindings(p *apiPages) []application.Binding {
	shape := func(codes ...application.Code) application.APIShape {
		return application.APIShape{Area: "schedules", Errors: codes}
	}
	cancel := shape(application.NotFound, application.Refused)
	cancel.Success = 204
	cancel.Body = application.BodyNone
	created := shape(application.Invalid, application.Refused)
	created.Success = 201
	patch := shape(application.Invalid, application.NotFound, application.Refused, application.Precondition)
	patch.Description = "Changes any of schedule, prompt, name and enabled. If-Match is optional: send the schedule's revision to refuse the change with 412 when it changed since it was read; without it the change applies as before."
	runNow := shape(application.NotFound, application.Conflict, application.Unavailable)
	runNow.Success = 202
	runNow.Body = application.BodyNone
	runNow.Description = "Asks the running serve to run the schedule now, through the same pause, module and budget checks as a scheduled run, and answers at once with the requested run."
	return []application.Binding{
		apiQuery(p, asstapp.DescribeScheduleList(), shape(application.Invalid), "GET /schedules",
			func(ctx context.Context, svc *asstapp.Service, in apiScheduleList) (application.Read[apiSchedules], error) {
				read, err := svc.ScheduleList(ctx, asstapp.ScheduleListInput{})
				if err != nil {
					return application.Read[apiSchedules]{}, err
				}
				page, err := pageAfter(read.Data.Jobs, func(j asstapp.ScheduleJob) int64 { return j.ID }, in.Cursor, in.Limit)
				if err != nil {
					return application.Read[apiSchedules]{}, err
				}
				return application.Read[apiSchedules]{Revision: read.Revision, Data: apiSchedules{Items: page.Items, NextCursor: page.NextCursor, TZ: read.Data.TZ}}, nil
			}),
		apiQuery(p, asstapp.DescribeScheduleRuns(), shape(application.Invalid), "GET /schedules/runs",
			func(ctx context.Context, svc *asstapp.Service, in apiScheduleList) (application.Read[web.List[asstapp.ScheduleRun]], error) {
				return scheduleRuns(ctx, svc, 0, in.Limit, in.Cursor)
			}),
		apiQuery(p, asstapp.DescribeScheduleHistory(), shape(application.Invalid, application.NotFound), "GET /schedules/{job_id}/runs",
			func(ctx context.Context, svc *asstapp.Service, in apiScheduleRuns) (application.Read[web.List[asstapp.ScheduleRun]], error) {
				return scheduleRuns(ctx, svc, in.JobID, in.Limit, in.Cursor)
			}),
		apiCommand(p, asstapp.DescribeScheduleAdd(), created, "POST /schedules",
			func(ctx context.Context, svc *asstapp.Service, in apiScheduleCreate) (application.Change[apiScheduleChanged], error) {
				result, err := svc.ScheduleAdd(ctx, asstapp.ScheduleAddInput{Name: in.Name, Prompt: in.Prompt, Schedule: in.Schedule})
				return changeAs(result, err, scheduleChanged)
			}),
		apiCommand(p, asstapp.DescribeScheduleUpdate(), patch, "PATCH /schedules/{job_id}",
			func(ctx context.Context, svc *asstapp.Service, in apiSchedulePatch) (application.Change[apiScheduleChanged], error) {
				if in.Schedule == nil && in.Prompt == nil && in.Name == nil && in.Enabled == nil {
					return application.Change[apiScheduleChanged]{}, invalidField("schedule", "Give a schedule, prompt, name or enabled to change.")
				}
				if in.Schedule != nil && strings.TrimSpace(*in.Schedule) == "" {
					return application.Change[apiScheduleChanged]{}, invalidField("schedule", "Give the schedule a cron expression.")
				}
				result, err := svc.ScheduleUpdate(ctx, asstapp.ScheduleUpdateInput{ID: in.JobID, Schedule: in.Schedule, Prompt: in.Prompt, Name: in.Name, Enabled: in.Enabled, IfMatch: in.IfMatch})
				return changeAs(result, err, scheduleChanged)
			}),
		apiCommand(p, asstapp.DescribeScheduleRunNow(), runNow, "POST /schedules/{job_id}/run",
			func(ctx context.Context, svc *asstapp.Service, in apiScheduleID) (application.Change[asstapp.ScheduleRunStarted], error) {
				return svc.ScheduleRunNow(ctx, asstapp.ScheduleRunNowInput{ID: in.JobID})
			}),
		apiCommand(p, asstapp.DescribeScheduleEnabled(), shape(application.Invalid, application.NotFound, application.Refused), "PUT /schedules/{job_id}/enabled",
			func(ctx context.Context, svc *asstapp.Service, in apiScheduleEnabled) (application.Change[apiScheduleChanged], error) {
				if in.Enabled == nil {
					return application.Change[apiScheduleChanged]{}, invalidField("enabled", "Say whether the schedule is enabled.")
				}
				result, err := svc.ScheduleEnabled(ctx, asstapp.ScheduleToggleInput{ID: in.JobID, Enabled: in.Enabled})
				return changeAs(result, err, scheduleChanged)
			}),
		apiCommand(p, asstapp.DescribeScheduleCancel(), cancel, "DELETE /schedules/{job_id}",
			func(ctx context.Context, svc *asstapp.Service, in apiScheduleID) (application.Change[apiScheduleChanged], error) {
				result, err := svc.ScheduleCancel(ctx, asstapp.ScheduleCancelInput{ID: in.JobID})
				return changeAs(result, err, scheduleChanged)
			}),
	}
}

func scheduleRuns(ctx context.Context, svc *asstapp.Service, jobID int64, limit api.Limit, cursor api.Cursor) (application.Read[web.List[asstapp.ScheduleRun]], error) {
	var before int64
	if cursor.Set() {
		if err := cursor.Last(&before); err != nil {
			return application.Read[web.List[asstapp.ScheduleRun]]{}, err
		}
		if before <= 0 {
			return application.Read[web.List[asstapp.ScheduleRun]]{}, api.ErrBadCursor
		}
	}
	read, err := svc.ScheduleHistory(ctx, asstapp.ScheduleHistoryInput{JobID: jobID, Before: before, Limit: int(limit) + 1})
	if err != nil {
		return application.Read[web.List[asstapp.ScheduleRun]]{}, err
	}
	out := web.List[asstapp.ScheduleRun]{Items: read.Data}
	if len(out.Items) > int(limit) {
		out.Items = out.Items[:int(limit)]
		out.NextCursor = cursor.Next(out.Items[len(out.Items)-1].ID)
	}
	return application.Read[web.List[asstapp.ScheduleRun]]{Revision: read.Revision, Data: out}, nil
}
