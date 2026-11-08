package scheduletools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/timex"
)

const Help = "ISO datetime for a one-off ('2026-11-02T09:00'), 'daily HH:MM', 'weekly mon HH:MM', or a 5-field cron expression " +
	"('0 9 * * 1-5'). Times are local. Recurring schedules must be at least 1 hour apart."

var chatOnly = tool.Kinds{tool.Chat}

func raised(err error) error {
	return tool.Domain[scheduler.Error, tool.Error](err)
}

func describe(ctx context.Context, q db.Querier, job *scheduler.Job) (map[string]any, error) {
	var due any
	if job.NextDue != nil {
		loc, err := scheduler.Zone(job.TZ)
		if err != nil {
			return nil, tool.Error("invalid time zone")
		}
		sec, us := timex.SplitTimestamp(*job.NextDue)
		due = time.Unix(sec, us*1000).In(loc).Format("2006-01-02T15:04-07:00")
	}
	return map[string]any{"id": job.ID, "name": job.Name, "prompt": job.Prompt, "schedule": job.Schedule, "enabled": job.State.Active(), "next_due": due, "created_by": job.CreatedBy}, nil
}

func agentJob(ctx context.Context, q db.Querier, args tool.Args) (*scheduler.Job, error) {
	id, _, err := tool.IntArg(args, "id", true)
	if err != nil {
		return nil, err
	}
	job, err := scheduler.GetJob(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, tool.Error(fmt.Sprintf("no active schedule %d", id))
	}
	if job.Fixed() {
		return nil, tool.Error("built-in runs are managed by the operator")
	}
	return job, nil
}

func text(args tool.Args, name string) (*string, error) {
	s, ok, err := tool.TextArg(args, name, false)
	if err != nil || !ok {
		return nil, err
	}
	return &s, nil
}

func create(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
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
			Name: fields[0], Prompt: fields[1], Schedule: fields[2], TZ: config.From(ctx).TZName(tc.Env), Now: tc.Now(),
			CreatedBy: scheduler.Agent, PromptBy: scheduler.Agent, ThreadID: &tc.ThreadID,
		})
		if err != nil {
			return err
		}
		job, err = scheduler.GetJob(ctx, tx, id)
		if err != nil {
			return err
		}
		_, err = writes.Record(ctx, tx, tc.Undoers, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "schedule_create", Target: fmt.Sprintf("schedule:%d", id),
			After: map[string]any{"id": id, "name": fields[0], "schedule": fields[2]},
			Undo:  scheduleDeleteUndo{Op: "schedule_delete", ID: id},
		})
		return err
	})
	if err != nil {
		return nil, raised(err)
	}
	return describe(ctx, tc.DB.Read, job)
}

func update(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	var updated *scheduler.Job
	err := tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		job, err := agentJob(ctx, tx, args)
		if err != nil {
			return err
		}
		u := scheduler.JobUpdate{PromptBy: scheduler.Agent, Actor: scheduler.Agent}
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
		if err != nil {
			return err
		}
		_, err = writes.Record(ctx, tx, tc.Undoers, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "schedule_update", Target: fmt.Sprintf("schedule:%d", job.ID),
			Before: map[string]any{"name": job.Name, "prompt": job.Prompt, "schedule": job.Schedule, "enabled": job.State.Active()},
			After:  map[string]any{"name": updated.Name, "prompt": updated.Prompt, "schedule": updated.Schedule, "enabled": updated.State.Active()},
			Undo: scheduleRestoreUndo{
				Op: "schedule_restore", ID: job.ID, Name: job.Name, Prompt: job.Prompt, Schedule: job.Schedule,
				State: string(job.State), NextDue: job.NextDue,
			},
		})
		return err
	})
	if err != nil {
		return nil, raised(err)
	}
	return describe(ctx, tc.DB.Read, updated)
}

func cancel(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	var id int64
	err := tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		job, err := agentJob(ctx, tx, args)
		if err != nil {
			return err
		}
		id = job.ID
		if err := scheduler.CancelJob(ctx, tx, id); err != nil {
			return err
		}
		_, err = writes.Record(ctx, tx, tc.Undoers, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "schedule_cancel", Target: fmt.Sprintf("schedule:%d", id),
			Before: map[string]any{"enabled": true},
			After:  map[string]any{"enabled": false},
			Undo: scheduleRestoreUndo{
				Op: "schedule_restore", ID: job.ID, Name: job.Name, Prompt: job.Prompt, Schedule: job.Schedule,
				State: string(job.State), NextDue: job.NextDue,
			},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"cancelled": id}, nil
}

func list(ctx context.Context, tc *tool.Context, _ tool.Args) (any, error) {
	jobs, err := scheduler.ListJobs(ctx, tc.DB.Read, false)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for i := range jobs {
		if jobs[i].Fixed() {
			continue
		}
		d, err := describe(ctx, nil, &jobs[i].Job)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return map[string]any{"schedules": out}, nil
}

func Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name: "schedule_create",
			Description: "Create a scheduled run: at the given time a new thread starts with `prompt` and read, notify and propose tools only. " +
				"The prompt must be self-contained. " + Help + " At most 20 active schedules.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"name": {"type": "string"}, "prompt": {"type": "string"}, "schedule": {"type": "string", "description": ` + jsonText(Help) + `}}, "required": ["name", "prompt", "schedule"], "additionalProperties": false}`),
			Handler:     create,
			Effect:      tool.WriteUndo, Undo: "schedule_delete",
			Annotated: true,
			Contexts:  chatOnly,
		},
		{
			Name:        "schedule_update",
			Description: "Change a schedule's name, prompt, schedule or enabled flag by id.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"id": {"type": "integer"}, "name": {"type": "string"}, "prompt": {"type": "string"}, "schedule": {"type": "string", "description": ` + jsonText(Help) + `}, "enabled": {"type": "boolean"}}, "required": ["id"], "additionalProperties": false}`),
			Handler:     update,
			Effect:      tool.WriteUndo, Undo: "schedule_restore",
			Annotated: true,
			Contexts:  chatOnly,
		},
		{
			Name:        "schedule_cancel",
			Description: "Cancel a schedule by id. Its past runs and threads are kept.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"id": {"type": "integer"}}, "required": ["id"], "additionalProperties": false}`),
			Handler:     cancel,
			Effect:      tool.WriteUndo, Undo: "schedule_restore",
			Annotated: true,
			Contexts:  chatOnly,
		},
		{
			Name:        "schedule_list",
			Description: "List scheduled runs with their next due time.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "required": [], "additionalProperties": false}`),
			Handler:     list,
			Effect:      tool.Read,
			Annotated:   true,
			Contexts:    chatOnly,
		},
	}
}

func jsonText(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
