package assistantmodule

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	kbapp "github.com/jeiang/atrium/internal/kb/application"
	"github.com/jeiang/atrium/internal/tasks"
	tasksapp "github.com/jeiang/atrium/internal/tasks/application"
	"github.com/jeiang/atrium/internal/web"
)

const taskListDescription = "The task list of the Plan and Tasks pages. Order: open, then done, then dismissed; within each, tasks with a due date by due date, then tasks without one; ties by creation time and id. " +
	"`counts` ignore `status` and the page but apply the other filters. `due_from` and `due_to` are inclusive dates (YYYY-MM-DD) compared with the first ten characters of `due` and leave out tasks without a due date. " +
	"`link` is `<kind>:<id>` and keeps the tasks linked to that subject. `q` matches title and notes. Refetch on the `tasks` topic."

type TaskListRequest struct {
	Status  string     `query:"status" json:"-" api:"enum=open|done|dismissed"`
	DueFrom string     `query:"due_from" json:"-"`
	DueTo   string     `query:"due_to" json:"-"`
	Link    string     `query:"link" json:"-"`
	Q       string     `query:"q" json:"-"`
	Cursor  api.Cursor `query:"cursor" json:"-"`
	Limit   api.Limit  `query:"limit" json:"-"`
}

type apiTask struct {
	ID         int64        `json:"id"`
	Ref        string       `json:"ref"`
	Title      string       `json:"title"`
	Notes      string       `json:"notes"`
	Due        *string      `json:"due"`
	RemindAt   *float64     `json:"remind_at"`
	Status     string       `json:"status" api:"enum=open|done|dismissed"`
	Priority   string       `json:"priority"`
	SourceKind string       `json:"source_kind"`
	SourceID   string       `json:"source_id"`
	CreatedTS  float64      `json:"created_ts"`
	UpdatedTS  float64      `json:"updated_ts"`
	DoneTS     *float64     `json:"done_ts"`
	Links      []tasks.Link `json:"links"`
}

type TaskPage struct {
	Items      []apiTask    `json:"items"`
	Marks      []string     `json:"marks"`
	Counts     tasks.Counts `json:"counts"`
	NextCursor *string      `json:"next_cursor" api:"nullable"`
}

type TaskLinkRequest struct {
	Kind string `json:"kind" api:"enum=thread|project|person|record|message"`
	ID   string `json:"id"`
}

type TaskAddRequest struct {
	Title    string           `json:"title"`
	Notes    string           `json:"notes,omitempty"`
	Due      *string          `json:"due,omitempty"`
	RemindAt *float64         `json:"remind_at,omitempty"`
	Priority string           `json:"priority,omitempty"`
	Link     *TaskLinkRequest `json:"link,omitempty"`
}

type TaskUpdateRequest struct {
	ID        int64            `path:"task_id" json:"-"`
	Title     *string          `json:"title,omitempty"`
	Notes     *string          `json:"notes,omitempty"`
	Due       *string          `json:"due,omitempty"`
	RemindAt  *float64         `json:"remind_at,omitempty"`
	Priority  *string          `json:"priority,omitempty"`
	Link      *TaskLinkRequest `json:"link,omitempty"`
	Clear     []string         `json:"clear,omitempty"`
	UpdatedTS *float64         `json:"updated_ts,omitempty"`
}

type TaskIDRequest struct {
	ID int64 `path:"task_id" json:"-"`
}

type TaskResponse struct {
	Task apiTask `json:"task"`
}

type TaskChanged struct {
	TaskID int64    `json:"task_id"`
	Status string   `json:"status,omitempty"`
	Task   *apiTask `json:"task,omitempty"`
}

func taskOf(t tasks.Task, links []tasks.Link) apiTask {
	if links == nil {
		links = []tasks.Link{}
	}
	return apiTask{ID: t.ID, Ref: "task:" + tasks.IDString(t.ID), Title: t.Title, Notes: t.Notes, Due: t.Due, RemindAt: t.RemindAt,
		Status: t.Status, Priority: t.Priority, SourceKind: t.SourceKind, SourceID: t.SourceID,
		CreatedTS: t.CreatedTS, UpdatedTS: t.UpdatedTS, DoneTS: t.DoneTS, Links: links}
}

func taskFieldError(name, message string) error {
	return &application.Error{Code: application.Invalid, Message: message,
		Fields: []application.Field{{Name: name, Code: "invalid", Message: message}}}
}

func taskFilter(in TaskListRequest) (tasks.Filter, error) {
	f := tasks.Filter{DueFrom: in.DueFrom, DueTo: in.DueTo, Text: in.Q}
	for name, value := range map[string]string{"due_from": in.DueFrom, "due_to": in.DueTo} {
		if value == "" {
			continue
		}
		if _, err := time.Parse(time.DateOnly, value); err != nil {
			return tasks.Filter{}, taskFieldError(name, name+" must be a date (YYYY-MM-DD)")
		}
	}
	if in.Link != "" {
		kind, id, ok := strings.Cut(in.Link, ":")
		if !ok || kind == "" || id == "" {
			return tasks.Filter{}, taskFieldError("link", "link must be <kind>:<id>")
		}
		f.LinkKind, f.LinkID = kind, id
	}
	return f, nil
}

func linkInput(l *TaskLinkRequest) *tasksapp.LinkInput {
	if l == nil {
		return nil
	}
	return &tasksapp.LinkInput{Kind: l.Kind, ID: l.ID}
}

func (m Module) taskRoutes(d *web.Deps) []application.Binding {
	svc := func() *tasksapp.Service {
		jobs, err := m.Jobs.Service(d.Ctx)
		if err != nil {
			panic(err)
		}
		return tasksapp.New(d.DB, jobs.Runner.Ledger, assistant.Now)
	}
	change := func(c application.Change[tasksapp.Changed], err error) (application.Change[TaskChanged], error) {
		if err != nil {
			return application.Change[TaskChanged]{}, err
		}
		out := application.Change[TaskChanged]{Data: TaskChanged{TaskID: c.Data.TaskID, Status: c.Data.Status}, EffectIDs: c.EffectIDs, AuditID: c.AuditID}
		if c.Data.Task != nil {
			task := taskOf(*c.Data.Task, c.Data.Links)
			out.Data.Task = &task
		}
		return out, nil
	}
	const area application.Area = "tasks"
	transition := application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.NotFound, application.Conflict}}
	return []application.Binding{
		api.Query(d, tasksapp.DescribeList(), application.APIShape{Area: area, Description: taskListDescription, Errors: []application.Code{application.Invalid}},
			"GET /tasks", func(ctx context.Context, in TaskListRequest) (application.Read[TaskPage], error) {
				filter, err := taskFilter(in)
				if err != nil {
					return application.Read[TaskPage]{}, err
				}
				list := tasksapp.ListInput{Status: in.Status, Filter: filter, Limit: int(in.Limit) + 1, Links: true}
				if in.Cursor.Set() {
					var after tasks.PageKey
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
				page := TaskPage{Items: []apiTask{}, Marks: strs(kbapp.MarkNames(read.Data.Marks)), Counts: read.Data.Counts}
				if len(found) > int(in.Limit) {
					found = found[:in.Limit]
					page.NextCursor = in.Cursor.Next(tasks.PageKeyOf(found[len(found)-1]))
				}
				for _, t := range found {
					page.Items = append(page.Items, taskOf(t, read.Data.Links[t.ID]))
				}
				return application.Read[TaskPage]{Revision: read.Revision, Data: page}, nil
			}),
		api.Query(d, tasksapp.DescribeGet(), application.APIShape{Area: area, Errors: []application.Code{application.NotFound}},
			"GET /tasks/{task_id}", func(ctx context.Context, in TaskIDRequest) (application.Read[TaskResponse], error) {
				read, err := svc().Get(ctx, tasksapp.IDInput{ID: in.ID})
				if err != nil {
					return application.Read[TaskResponse]{}, err
				}
				return application.Read[TaskResponse]{Revision: read.Revision, Data: TaskResponse{Task: taskOf(read.Data.Task, read.Data.Links)}}, nil
			}),
		api.Command(d, tasksapp.DescribeAdd(), application.APIShape{Area: area, Success: http.StatusCreated, Errors: []application.Code{application.Invalid}},
			"POST /tasks", func(ctx context.Context, in TaskAddRequest) (application.Change[TaskChanged], error) {
				return change(svc().Add(ctx, tasksapp.AddInput{Title: in.Title, Notes: in.Notes, Due: in.Due, RemindAt: in.RemindAt, Priority: in.Priority, Link: linkInput(in.Link)}))
			}),
		api.Command(d, tasksapp.DescribeUpdate(), application.APIShape{Area: area, Errors: []application.Code{application.NotFound, application.Invalid, application.Conflict}},
			"PATCH /tasks/{task_id}", func(ctx context.Context, in TaskUpdateRequest) (application.Change[TaskChanged], error) {
				return change(svc().Update(ctx, tasksapp.UpdateInput{ID: in.ID, Title: in.Title, Notes: in.Notes, Due: in.Due, RemindAt: in.RemindAt, Priority: in.Priority,
					Link: linkInput(in.Link), Clear: in.Clear, UpdatedTS: in.UpdatedTS}))
			}),
		api.Command(d, tasksapp.DescribeDone(), transition,
			"POST /tasks/{task_id}/done", func(ctx context.Context, in TaskIDRequest) (application.Change[TaskChanged], error) {
				return change(svc().Done(ctx, tasksapp.IDInput{ID: in.ID}))
			}),
		api.Command(d, tasksapp.DescribeReopen(), transition,
			"POST /tasks/{task_id}/reopen", func(ctx context.Context, in TaskIDRequest) (application.Change[TaskChanged], error) {
				return change(svc().Reopen(ctx, tasksapp.IDInput{ID: in.ID}))
			}),
		api.Command(d, tasksapp.DescribeDismiss(), transition,
			"POST /tasks/{task_id}/dismiss", func(ctx context.Context, in TaskIDRequest) (application.Change[TaskChanged], error) {
				return change(svc().Dismiss(ctx, tasksapp.IDInput{ID: in.ID}))
			}),
		api.Command(d, tasksapp.DescribeDelete(), application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.NotFound}},
			"DELETE /tasks/{task_id}", func(ctx context.Context, in TaskIDRequest) (application.Change[TaskChanged], error) {
				return change(svc().Delete(ctx, tasksapp.IDInput{ID: in.ID}))
			}),
	}
}
