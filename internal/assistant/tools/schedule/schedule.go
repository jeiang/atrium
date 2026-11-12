package scheduletools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/timex"
	"github.com/jeiang/atrium/internal/tool"
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
	_, err := tc.Ledger.ApplyLocal(ctx, ledger.New{
		Origin: "agent", Operation: "schedule_create", OperationKey: "schedule_create:" + ledger.NewKey(),
		Target: "schedule:new", ThreadID: &tc.ThreadID, RunID: &tc.RunID, ProposalID: tc.ProposalID,
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		id, err := scheduler.CreateJob(ctx, tx, scheduler.NewJob{
			Name: fields[0], Prompt: fields[1], Schedule: fields[2], TZ: config.From(ctx).TZName(tc.Env), Now: tc.Now(),
			CreatedBy: scheduler.Agent, PromptBy: scheduler.Agent, ThreadID: &tc.ThreadID,
		})
		if err != nil {
			return ledger.Result{}, err
		}
		job, err = scheduler.GetJob(ctx, tx, id)
		if err != nil {
			return ledger.Result{}, err
		}
		return ledger.Result{
			State:  ledger.Done,
			Target: fmt.Sprintf("schedule:%d", id),
			After:  jsonOf(map[string]any{"id": id, "name": fields[0], "schedule": fields[2]}),
			Undo:   jsonOf(scheduleDeleteUndo{Op: "schedule_delete", ID: id}),
		}, nil
	})
	if err != nil {
		return nil, raised(err)
	}
	return describe(ctx, tc.DB.Read, job)
}

func update(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	var updated *scheduler.Job
	_, err := tc.Ledger.ApplyLocal(ctx, ledger.New{
		Origin: "agent", Operation: "schedule_update", OperationKey: "schedule_update:" + ledger.NewKey(),
		Target: "schedule:new", ThreadID: &tc.ThreadID, RunID: &tc.RunID, ProposalID: tc.ProposalID,
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		job, err := agentJob(ctx, tx, args)
		if err != nil {
			return ledger.Result{}, err
		}
		u := scheduler.JobUpdate{PromptBy: scheduler.Agent, Actor: scheduler.Agent}
		if raw, _ := args.Get("enabled"); raw != nil {
			b, ok := raw.(bool)
			if !ok {
				return ledger.Result{}, tool.Error("enabled must be a boolean")
			}
			u.Enabled = &b
		}
		if u.Name, err = text(args, "name"); err != nil {
			return ledger.Result{}, err
		}
		if u.Prompt, err = text(args, "prompt"); err != nil {
			return ledger.Result{}, err
		}
		if u.Schedule, err = text(args, "schedule"); err != nil {
			return ledger.Result{}, err
		}
		updated, err = scheduler.UpdateJob(ctx, tx, job.ID, tc.Now(), u)
		if err != nil {
			return ledger.Result{}, err
		}
		return ledger.Result{
			State:  ledger.Done,
			Target: fmt.Sprintf("schedule:%d", job.ID),
			Before: jsonOf(map[string]any{"name": job.Name, "prompt": job.Prompt, "schedule": job.Schedule, "enabled": job.State.Active()}),
			After:  jsonOf(map[string]any{"name": updated.Name, "prompt": updated.Prompt, "schedule": updated.Schedule, "enabled": updated.State.Active()}),
			Undo: jsonOf(scheduleRestoreUndo{
				Op: "schedule_restore", ID: job.ID, Name: job.Name, Prompt: job.Prompt, Schedule: job.Schedule,
				State: string(job.State), NextDue: job.NextDue,
			}),
		}, nil
	})
	if err != nil {
		return nil, raised(err)
	}
	return describe(ctx, tc.DB.Read, updated)
}

func cancel(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	var id int64
	_, err := tc.Ledger.ApplyLocal(ctx, ledger.New{
		Origin: "agent", Operation: "schedule_cancel", OperationKey: "schedule_cancel:" + ledger.NewKey(),
		Target: "schedule:new", ThreadID: &tc.ThreadID, RunID: &tc.RunID, ProposalID: tc.ProposalID,
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		job, err := agentJob(ctx, tx, args)
		if err != nil {
			return ledger.Result{}, err
		}
		id = job.ID
		if err := scheduler.CancelJob(ctx, tx, id); err != nil {
			return ledger.Result{}, err
		}
		return ledger.Result{
			State:  ledger.Done,
			Target: fmt.Sprintf("schedule:%d", id),
			Before: jsonOf(map[string]any{"enabled": true}),
			After:  jsonOf(map[string]any{"enabled": false}),
			Undo: jsonOf(scheduleRestoreUndo{
				Op: "schedule_restore", ID: job.ID, Name: job.Name, Prompt: job.Prompt, Schedule: job.Schedule,
				State: string(job.State), NextDue: job.NextDue,
			}),
		}, nil
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
	targets := []string{}
	for i := range jobs {
		if jobs[i].Fixed() {
			continue
		}
		d, err := describe(ctx, nil, &jobs[i].Job)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
		targets = append(targets, fmt.Sprintf("schedule:%d", jobs[i].ID))
	}
	marks, err := threads.MarksOf(ctx, tc.DB.Read, targets)
	if err != nil {
		return nil, err
	}
	return tool.Marked{Value: map[string]any{"schedules": out}, Marks: marks}, nil
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
			Channel:   tool.ChannelOwn,
			Contexts:  chatOnly,
		},
		{
			Name:        "schedule_update",
			Description: "Change a schedule's name, prompt, schedule or enabled flag by id.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"id": {"type": "integer"}, "name": {"type": "string"}, "prompt": {"type": "string"}, "schedule": {"type": "string", "description": ` + jsonText(Help) + `}, "enabled": {"type": "boolean"}}, "required": ["id"], "additionalProperties": false}`),
			Handler:     update,
			Effect:      tool.WriteUndo, Undo: "schedule_restore",
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  chatOnly,
		},
		{
			Name:        "schedule_cancel",
			Description: "Cancel a schedule by id. Its past runs and threads are kept.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"id": {"type": "integer"}}, "required": ["id"], "additionalProperties": false}`),
			Handler:     cancel,
			Effect:      tool.WriteUndo, Undo: "schedule_restore",
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  chatOnly,
		},
		{
			Name:        "schedule_list",
			Description: "List scheduled runs with their next due time.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "required": [], "additionalProperties": false}`),
			Handler:     list,
			Effect:      tool.Read,
			Annotated:   true,
			Marks:       tool.AllMarks,
			Contexts:    chatOnly,
		},
	}
}

func jsonText(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
