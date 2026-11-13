package assistantmodule

import (
	"context"
	"net/http"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	kbapp "github.com/jeiang/atrium/internal/kb/application"
	"github.com/jeiang/atrium/internal/tasks"
	tasksapp "github.com/jeiang/atrium/internal/tasks/application"
	"github.com/jeiang/atrium/internal/web"
)

type TaskListRequest struct {
	Status string     `query:"status" json:"-" api:"enum=open|done"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}

type TaskPage struct {
	Items      []tasks.Task `json:"items"`
	Marks      []string     `json:"marks"`
	NextCursor *string      `json:"next_cursor" api:"nullable"`
}

type TaskAddRequest struct {
	Title    string   `json:"title"`
	Notes    string   `json:"notes,omitempty"`
	Due      *string  `json:"due,omitempty"`
	RemindAt *float64 `json:"remind_at,omitempty"`
	Priority string   `json:"priority,omitempty"`
}

type TaskUpdateRequest struct {
	ID       int64    `path:"task_id" json:"-"`
	Title    *string  `json:"title,omitempty"`
	Notes    *string  `json:"notes,omitempty"`
	Due      *string  `json:"due,omitempty"`
	RemindAt *float64 `json:"remind_at,omitempty"`
	Priority *string  `json:"priority,omitempty"`
}

type TaskIDRequest struct {
	ID int64 `path:"task_id" json:"-"`
}

type TaskChanged struct {
	TaskID int64  `json:"task_id"`
	Status string `json:"status,omitempty"`
}

func (m Module) taskRoutes(d *web.Deps) []application.Binding {
	svc := func() *tasksapp.Service {
		jobs, err := m.Jobs.Service(d.Ctx)
		if err != nil {
			panic(err)
		}
		return tasksapp.New(d.DB, jobs.Runner.Ledger, assistant.Now)
	}
	const area application.Area = "tasks"
	return []application.Binding{
		api.Query(d, tasksapp.DescribeList(), application.APIShape{Area: area},
			"GET /tasks", func(ctx context.Context, in TaskListRequest) (application.Read[TaskPage], error) {
				list := tasksapp.ListInput{Status: in.Status, Limit: int(in.Limit) + 1}
				if in.Cursor.Set() {
					var after tasks.ListKey
					if err := in.Cursor.Last(&after); err != nil {
						return application.Read[TaskPage]{}, err
					}
					list.After = &after
				}
				read, err := svc().List(ctx, list)
				if err != nil {
					return application.Read[TaskPage]{}, err
				}
				found := read.Data.Tasks
				page := TaskPage{Items: []tasks.Task{}, Marks: strs(kbapp.MarkNames(read.Data.Marks))}
				if len(found) > int(in.Limit) {
					found = found[:in.Limit]
					page.NextCursor = in.Cursor.Next(tasks.KeyOf(found[len(found)-1]))
				}
				page.Items = append(page.Items, found...)
				return application.Read[TaskPage]{Revision: read.Revision, Data: page}, nil
			}),
		api.Command(d, tasksapp.DescribeAdd(), application.APIShape{Area: area, Success: http.StatusCreated, Errors: []application.Code{application.Invalid}},
			"POST /tasks", func(ctx context.Context, in TaskAddRequest) (application.Change[TaskChanged], error) {
				return taskChange(svc().Add(ctx, tasksapp.AddInput{Title: in.Title, Notes: in.Notes, Due: in.Due, RemindAt: in.RemindAt, Priority: in.Priority}))
			}),
		api.Command(d, tasksapp.DescribeUpdate(), application.APIShape{Area: area, Errors: []application.Code{application.NotFound}},
			"PATCH /tasks/{task_id}", func(ctx context.Context, in TaskUpdateRequest) (application.Change[TaskChanged], error) {
				return taskChange(svc().Update(ctx, tasksapp.UpdateInput{ID: in.ID, Title: in.Title, Notes: in.Notes, Due: in.Due, RemindAt: in.RemindAt, Priority: in.Priority}))
			}),
		api.Command(d, tasksapp.DescribeDone(), application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.NotFound}},
			"POST /tasks/{task_id}/done", func(ctx context.Context, in TaskIDRequest) (application.Change[TaskChanged], error) {
				return taskChange(svc().Done(ctx, tasksapp.IDInput{ID: in.ID}))
			}),
	}
}

func taskChange(c application.Change[tasksapp.Changed], err error) (application.Change[TaskChanged], error) {
	if err != nil {
		return application.Change[TaskChanged]{}, err
	}
	return application.Change[TaskChanged]{Data: TaskChanged{TaskID: c.Data.TaskID, Status: c.Data.Status}, EffectIDs: c.EffectIDs, AuditID: c.AuditID}, nil
}
