package scheduletools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/skills"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

const Help = "ISO datetime for a one-off ('2026-11-02T09:00'), 'daily HH:MM', 'weekly mon HH:MM', or a 5-field cron expression " +
	"('0 9 * * 1-5'). Times are local. Recurring schedules must be at least 1 hour apart."

var chatOnly = tool.Kinds{tool.Chat}

func raised(err error) error {
	var se scheduler.Error
	if errors.As(err, &se) {
		return tool.Error(se)
	}
	return err
}

func describe(ctx context.Context, q db.Querier, job *scheduler.Job) (py.Dict, error) {
	var due any
	if job.NextDue != nil {
		loc, err := scheduler.Zone(job.TZ)
		if err != nil {
			return nil, &tool.Raised{Class: "ScheduleError"}
		}
		sec, us := py.SplitTimestamp(*job.NextDue)
		due = py.ISOFormat(time.Unix(sec, us*1000).In(loc), "minutes")
	}
	found := py.Dict{
		{Key: "id", Value: job.ID},
		{Key: "name", Value: job.Name},
		{Key: "prompt", Value: job.Prompt},
		{Key: "schedule", Value: job.Schedule},
		{Key: "enabled", Value: job.Enabled},
		{Key: "next_due", Value: due},
		{Key: "created_by", Value: job.CreatedBy},
	}
	if q == nil {
		return found, nil
	}
	refs := skills.References(job.Prompt)
	if len(refs) == 0 {
		return found, nil
	}
	known, unknown, err := skills.SplitKnown(ctx, q, refs)
	if err != nil {
		return nil, err
	}
	found = append(found, py.Pair{Key: "skills", Value: known})
	if len(unknown) > 0 {
		found = append(found, py.Pair{Key: "note", Value: "unknown skills in prompt (left as text): " + strings.Join(unknown, ", ")})
	}
	return found, nil
}

func agentJob(ctx context.Context, q db.Querier, args py.Dict) (*scheduler.Job, error) {
	raw, _ := args.Get("id")
	var id int64
	switch v := raw.(type) {
	case int64:
		id = v
	case *big.Int:
		return nil, &tool.Raised{Class: "OverflowError"}
	default:
		return nil, tool.Error("id must be an integer")
	}
	job, err := scheduler.GetJob(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if job == nil || job.Cancelled {
		return nil, tool.Error(fmt.Sprintf("no active schedule %d", id))
	}
	if job.Kind == scheduler.JobNightly || job.Kind == scheduler.JobBuiltin {
		return nil, tool.Error("built-in runs are managed by the operator")
	}
	return job, nil
}

func text(args py.Dict, name string) (*string, error) {
	s, ok, err := tool.TextArg(args, name, false)
	if err != nil || !ok {
		return nil, err
	}
	return &s, nil
}

func create(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	var fields [3]string
	for i, name := range []string{"name", "prompt", "schedule"} {
		s, _, err := tool.TextArg(args, name, true)
		if err != nil {
			return nil, err
		}
		fields[i] = s
	}
	var job *scheduler.Job
	err := tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		id, err := scheduler.CreateJob(ctx, tx, scheduler.NewJob{
			Name: fields[0], Prompt: fields[1], Schedule: fields[2], TZ: config.TZName(tc.Env), Now: tc.Now(),
			CreatedBy: scheduler.Agent, PromptBy: scheduler.Agent, ThreadID: &tc.ThreadID,
		})
		if err != nil {
			return err
		}
		job, err = scheduler.GetJob(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, raised(err)
	}
	return describe(ctx, tc.DB.Read, job)
}

func update(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	var updated *scheduler.Job
	err := tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		job, err := agentJob(ctx, tx, args)
		if err != nil {
			return err
		}
		u := scheduler.JobUpdate{PromptBy: scheduler.Agent}
		if raw, _ := args.Get("enabled"); raw != nil {
			b, ok := raw.(bool)
			if !ok {
				return tool.Error("enabled must be a boolean")
			}
			u.Enabled = &b
		}
		if u.Name, err = text(args, "name"); err != nil {
			return err
		}
		if u.Prompt, err = text(args, "prompt"); err != nil {
			return err
		}
		if u.Schedule, err = text(args, "schedule"); err != nil {
			return err
		}
		updated, err = scheduler.UpdateJob(ctx, tx, job.ID, tc.Now(), u)
		return err
	})
	if err != nil {
		return nil, raised(err)
	}
	return describe(ctx, tc.DB.Read, updated)
}

func cancel(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	var id int64
	err := tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		job, err := agentJob(ctx, tx, args)
		if err != nil {
			return err
		}
		id = job.ID
		return scheduler.CancelJob(ctx, tx, id)
	})
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "cancelled", Value: id}}, nil
}

func list(ctx context.Context, tc *tool.Context, _ py.Dict) (any, error) {
	jobs, err := scheduler.ListJobs(ctx, tc.DB.Read, false)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for i := range jobs {
		if jobs[i].Kind == scheduler.JobNightly || jobs[i].Kind == scheduler.JobBuiltin {
			continue
		}
		d, err := describe(ctx, nil, &jobs[i].Job)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return py.Dict{{Key: "schedules", Value: out}}, nil
}

func Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name: "schedule_create",
			Description: "Create a scheduled run: at the given time a new thread starts with `prompt` and read, notify and propose tools only. " +
				"The prompt must be self-contained. " + Help + " At most 20 active schedules.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"name": {"type": "string"}, "prompt": {"type": "string"}, "schedule": {"type": "string", "description": ` + py.JSONDumps(Help) + `}}, "required": ["name", "prompt", "schedule"], "additionalProperties": false}`),
			Handler:     create,
			Effect:      tool.Write,
			Contexts:    chatOnly,
		},
		{
			Name:        "schedule_update",
			Description: "Change a schedule's name, prompt, schedule or enabled flag by id.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"id": {"type": "integer"}, "name": {"type": "string"}, "prompt": {"type": "string"}, "schedule": {"type": "string", "description": ` + py.JSONDumps(Help) + `}, "enabled": {"type": "boolean"}}, "required": ["id"], "additionalProperties": false}`),
			Handler:     update,
			Effect:      tool.Write,
			Contexts:    chatOnly,
		},
		{
			Name:        "schedule_cancel",
			Description: "Cancel a schedule by id. Its past runs and threads are kept.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"id": {"type": "integer"}}, "required": ["id"], "additionalProperties": false}`),
			Handler:     cancel,
			Effect:      tool.Write,
			Contexts:    chatOnly,
		},
		{
			Name:        "schedule_list",
			Description: "List scheduled runs with their next due time.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "required": [], "additionalProperties": false}`),
			Handler:     list,
			Effect:      tool.Read,
			Contexts:    chatOnly,
		},
	}
}
