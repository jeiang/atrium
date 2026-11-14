package scheduletools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/tool"
)

const Help = "ISO datetime for a one-off ('2026-11-02T09:00'), 'daily HH:MM', 'weekly mon HH:MM', or a 5-field cron expression " +
	"('0 9 * * 1-5'). Times are local. Recurring schedules must be at least 1 hour apart."

var chatOnly = tool.Kinds{tool.Chat}

type Service func(*tool.Context) *asstapp.Service

func fail(err error) error {
	var e *application.Error
	if errors.As(err, &e) {
		return tool.Error(e.Message)
	}
	return err
}

func describe(job *asstapp.ScheduleJob) map[string]any {
	var due any
	if job.NextDue != nil {
		due = *job.NextDue
	}
	return map[string]any{"id": job.ID, "name": job.Name, "prompt": job.Prompt, "schedule": job.Schedule, "enabled": job.Enabled, "next_due": due, "created_by": job.CreatedBy}
}

func source(tc *tool.Context) *asstapp.ScheduleSource {
	return &asstapp.ScheduleSource{ThreadID: tc.ThreadID, RunID: tc.RunID, ProposalID: tc.ProposalID}
}

func text(args tool.Args, name string) (*string, error) {
	s, ok, err := tool.TextArg(args, name, false)
	if err != nil || !ok {
		return nil, err
	}
	return &s, nil
}

func create(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		var fields [3]string
		for i, name := range []string{"name", "prompt", "schedule"} {
			s, _, err := tool.TextArg(args, name, true)
			if err != nil {
				return nil, err
			}
			fields[i] = s
		}
		got, err := svc(tc).ScheduleCreate(ctx, asstapp.ScheduleCreateInput{Source: *source(tc), Name: fields[0], Prompt: fields[1], Schedule: fields[2]})
		if err != nil {
			return nil, fail(err)
		}
		return describe(got.Data.Job), nil
	}
}

func update(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		id, _, err := tool.IntArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		in := asstapp.ScheduleUpdateInput{ID: id, Agent: source(tc)}
		if raw, _ := args.Get("enabled"); raw != nil {
			b, ok := raw.(bool)
			if !ok {
				return nil, tool.Error("enabled must be a boolean")
			}
			in.Enabled = &b
		}
		if in.Name, err = text(args, "name"); err != nil {
			return nil, err
		}
		if in.Prompt, err = text(args, "prompt"); err != nil {
			return nil, err
		}
		if in.Schedule, err = text(args, "schedule"); err != nil {
			return nil, err
		}
		got, err := svc(tc).ScheduleUpdate(ctx, in)
		if err != nil {
			return nil, fail(err)
		}
		return describe(got.Data.Job), nil
	}
}

func cancel(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		id, _, err := tool.IntArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		got, err := svc(tc).ScheduleCancel(ctx, asstapp.ScheduleCancelInput{ID: id, Agent: source(tc)})
		if err != nil {
			return nil, fail(err)
		}
		return map[string]any{"cancelled": got.Data.ID}, nil
	}
}

func list(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, _ tool.Args) (any, error) {
		got, err := svc(tc).ScheduleList(ctx, asstapp.ScheduleListInput{Editable: true})
		if err != nil {
			return nil, fail(err)
		}
		out := make([]any, len(got.Data.Jobs))
		for i := range got.Data.Jobs {
			out[i] = describe(&got.Data.Jobs[i])
		}
		return tool.Marked{Value: map[string]any{"schedules": out}, Marks: got.Data.Marks}, nil
	}
}

func Tools(svc Service) []tool.Tool {
	return []tool.Tool{
		{
			Name: "schedule_create",
			Description: "Create a scheduled run: at the given time a new thread starts with `prompt` and read, notify and propose tools only. " +
				"The prompt must be self-contained. " + Help + " At most 20 active schedules.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"name": {"type": "string"}, "prompt": {"type": "string"}, "schedule": {"type": "string", "description": ` + jsonText(Help) + `}}, "required": ["name", "prompt", "schedule"], "additionalProperties": false}`),
			Handler:     create(svc),
			Effect:      tool.WriteUndo, Undo: asstapp.ScheduleDeleteOp,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  chatOnly,
		},
		{
			Name:        "schedule_update",
			Description: "Change a schedule's name, prompt, schedule or enabled flag by id.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"id": {"type": "integer"}, "name": {"type": "string"}, "prompt": {"type": "string"}, "schedule": {"type": "string", "description": ` + jsonText(Help) + `}, "enabled": {"type": "boolean"}}, "required": ["id"], "additionalProperties": false}`),
			Handler:     update(svc),
			Effect:      tool.WriteUndo, Undo: asstapp.ScheduleRestoreOp,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  chatOnly,
		},
		{
			Name:        "schedule_cancel",
			Description: "Cancel a schedule by id. Its past runs and threads are kept.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"id": {"type": "integer"}}, "required": ["id"], "additionalProperties": false}`),
			Handler:     cancel(svc),
			Effect:      tool.WriteUndo, Undo: asstapp.ScheduleRestoreOp,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  chatOnly,
		},
		{
			Name:        "schedule_list",
			Description: "List scheduled runs with their next due time.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "required": [], "additionalProperties": false}`),
			Handler:     list(svc),
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
