package coremodule

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	commitmentsapp "github.com/jeiang/atrium/internal/commitments/application"
	kbapp "github.com/jeiang/atrium/internal/kb/application"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tasks"
	tasksapp "github.com/jeiang/atrium/internal/tasks/application"
	"github.com/jeiang/atrium/internal/web"
)

const commitmentTopics = "Refetch on an app event `change` for the topic `tasks` (tasks) or `mail` (commitments from mail)."

const commitmentListDescription = "To do and Waiting in one list: tasks and the commitments from mail the operator owes (`side` todo), and the commitments other people owe the operator (`side` waiting, `owner` them). " +
	"An item's `ref` is `task:<id>` or `commitment:<id>`, the same id `/queue` uses. Tasks live in the assistant module and commitments in the mail module; the items of a module that is off are left out. " +
	"Order: todo, then waiting; within each, open, then done, then dismissed; then items with a due date by due date, then items without one; ties by creation time, source and id. " +
	"`counts` ignore `side`, `status` and the page but apply the other filters. `due_from` and `due_to` are inclusive dates (YYYY-MM-DD) compared with the first ten characters of `due` and leave out items without a due date. " +
	"`link` is `<kind>:<id>`: tasks match their link, commitments match their `message` or reminder `proposal`. `q` matches a task's title and notes and a commitment's text. " +
	"`actions` lists the verbs the item accepts; a commitment accepts only status changes. " + commitmentTopics

const commitmentReadOnly = "A commitment from mail accepts only done, dismiss and reopen; edit and delete answer conflict with reason `read_only`. "

type CommitmentListRequest struct {
	Side    string     `query:"side" json:"-" api:"enum=todo|waiting"`
	Status  string     `query:"status" json:"-" api:"enum=open|done|dismissed"`
	DueFrom string     `query:"due_from" json:"-"`
	DueTo   string     `query:"due_to" json:"-"`
	Link    string     `query:"link" json:"-"`
	Q       string     `query:"q" json:"-"`
	Cursor  api.Cursor `query:"cursor" json:"-"`
	Limit   api.Limit  `query:"limit" json:"-"`
}

type CommitmentPage struct {
	Items      []commitmentsapp.CommitmentItem `json:"items"`
	Marks      []string                        `json:"marks"`
	Counts     commitmentsapp.CommitmentCounts `json:"counts"`
	NextCursor *string                         `json:"next_cursor" api:"nullable"`
}

type CommitmentRefRequest struct {
	Ref string `path:"ref" json:"-"`
}

type CommitmentLinkRequest struct {
	Kind string `json:"kind" api:"enum=thread|project|person|record|message"`
	ID   string `json:"id"`
}

type CommitmentUpdateRequest struct {
	Ref       string                 `path:"ref" json:"-"`
	Title     *string                `json:"title,omitempty"`
	Notes     *string                `json:"notes,omitempty"`
	Due       *string                `json:"due,omitempty"`
	RemindAt  *float64               `json:"remind_at,omitempty"`
	Priority  *string                `json:"priority,omitempty"`
	Link      *CommitmentLinkRequest `json:"link,omitempty"`
	Clear     []string               `json:"clear,omitempty"`
	UpdatedTS *float64               `json:"updated_ts,omitempty"`
}

type CommitmentResponse struct {
	Item commitmentsapp.CommitmentItem `json:"item"`
}

type CommitmentChanged struct {
	Ref    string                         `json:"ref"`
	Status string                         `json:"status"`
	Item   *commitmentsapp.CommitmentItem `json:"item,omitempty"`
}

func commitmentFieldError(name, message string) error {
	return &application.Error{Code: application.Invalid, Message: message,
		Fields: []application.Field{{Name: name, Code: "invalid", Message: message}}}
}

func commitmentFilter(in CommitmentListRequest) (tasks.Filter, error) {
	f := tasks.Filter{DueFrom: in.DueFrom, DueTo: in.DueTo, Text: in.Q}
	for name, value := range map[string]string{"due_from": in.DueFrom, "due_to": in.DueTo} {
		if value == "" {
			continue
		}
		if _, err := time.Parse(time.DateOnly, value); err != nil {
			return tasks.Filter{}, commitmentFieldError(name, name+" must be a date (YYYY-MM-DD)")
		}
	}
	if in.Link != "" {
		kind, id, ok := strings.Cut(in.Link, ":")
		if !ok || kind == "" || id == "" {
			return tasks.Filter{}, commitmentFieldError("link", "link must be <kind>:<id>")
		}
		f.LinkKind, f.LinkID = kind, id
	}
	return f, nil
}

func (m Module) commitmentService(d *web.Deps) func() *commitmentsapp.Service {
	return sync.OnceValue(func() *commitmentsapp.Service {
		open := imapclient.Opener(d.Cfg)
		if m.Clients != nil {
			open = m.Clients(d.Cfg)
		}
		now := m.Now
		if now == nil {
			now = mail.WallClock
		}
		store, err := module.SetFrom(d.Ctx).Catalog().Ledger(d.DB, d.Env, open, now)
		if err != nil {
			panic(err)
		}
		d.DrainOnStop(store.Drain)
		return commitmentsapp.New(d.DB, store, now)
	})
}

func describeCommitment(name application.Name) application.Definition {
	for _, def := range commitmentsapp.Describe() {
		if def.Name == name {
			return def
		}
	}
	panic("commitments: no definition for " + string(name))
}

func commitmentOperations() ([]application.Definition, []application.Binding) {
	return commitmentsapp.Describe(), commitmentRoutes(nil, nil)
}

func commitmentRoutes(d *web.Deps, svc func() *commitmentsapp.Service) []application.Binding {
	change := func(c application.Change[commitmentsapp.Changed], err error) (application.Change[CommitmentChanged], error) {
		if err != nil {
			return application.Change[CommitmentChanged]{}, err
		}
		return application.Change[CommitmentChanged]{Data: CommitmentChanged{Ref: c.Data.Ref, Status: c.Data.Status, Item: c.Data.Item}, EffectIDs: c.EffectIDs, AuditID: c.AuditID}, nil
	}
	const area application.Area = "tasks"
	transition := application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.Invalid, application.NotFound, application.Conflict}}
	verb := func(name application.Name, route string, run func(*commitmentsapp.Service, context.Context, commitmentsapp.RefInput) (application.Change[commitmentsapp.Changed], error)) application.Binding {
		return api.Command(d, describeCommitment(name), transition, route, func(ctx context.Context, in CommitmentRefRequest) (application.Change[CommitmentChanged], error) {
			return change(run(svc(), withModules(ctx, d), commitmentsapp.RefInput{Ref: in.Ref}))
		})
	}
	return []application.Binding{
		api.Query(d, describeCommitment(commitmentsapp.ListName), application.APIShape{Area: area, Description: commitmentListDescription, Errors: []application.Code{application.Invalid}},
			"GET /commitments", func(ctx context.Context, in CommitmentListRequest) (application.Read[CommitmentPage], error) {
				filter, err := commitmentFilter(in)
				if err != nil {
					return application.Read[CommitmentPage]{}, err
				}
				list := commitmentsapp.ListInput{Side: in.Side, Status: in.Status, Filter: filter, Limit: int(in.Limit)}
				if in.Cursor.Set() {
					list.After = &commitmentsapp.Key{}
					if err := in.Cursor.Last(list.After); err != nil {
						return application.Read[CommitmentPage]{}, err
					}
				}
				read, err := svc().List(withModules(ctx, d), list)
				if err != nil {
					return application.Read[CommitmentPage]{}, err
				}
				page := CommitmentPage{Items: read.Data.Items, Marks: kbapp.MarkNames(read.Data.Marks), Counts: read.Data.Counts}
				if read.Data.Next != nil {
					page.NextCursor = in.Cursor.Next(read.Data.Next)
				}
				return application.Read[CommitmentPage]{Revision: read.Revision, Data: page}, nil
			}),
		api.Query(d, describeCommitment(commitmentsapp.GetName), application.APIShape{Area: area, Description: "One To do or Waiting item by `ref`. " + commitmentTopics, Errors: []application.Code{application.Invalid, application.NotFound}},
			"GET /commitments/{ref}", func(ctx context.Context, in CommitmentRefRequest) (application.Read[CommitmentResponse], error) {
				read, err := svc().Get(withModules(ctx, d), commitmentsapp.RefInput{Ref: in.Ref})
				if err != nil {
					return application.Read[CommitmentResponse]{}, err
				}
				return application.Read[CommitmentResponse]{Revision: read.Revision, Data: CommitmentResponse{Item: read.Data}}, nil
			}),
		verb(commitmentsapp.DoneName, "POST /commitments/{ref}/done", (*commitmentsapp.Service).Done),
		verb(commitmentsapp.ReopenName, "POST /commitments/{ref}/reopen", (*commitmentsapp.Service).Reopen),
		verb(commitmentsapp.DismissName, "POST /commitments/{ref}/dismiss", (*commitmentsapp.Service).Dismiss),
		commitmentSnoozeRoute(d, svc),
		api.Command(d, describeCommitment(commitmentsapp.UpdateName), application.APIShape{Area: area, Description: "Edit a task by `ref`, as `PATCH /tasks/{task_id}` does. " + commitmentReadOnly + commitmentTopics,
			Errors: []application.Code{application.Invalid, application.NotFound, application.Conflict}},
			"PATCH /commitments/{ref}", func(ctx context.Context, in CommitmentUpdateRequest) (application.Change[CommitmentChanged], error) {
				var link *tasksapp.LinkInput
				if in.Link != nil {
					link = &tasksapp.LinkInput{Kind: in.Link.Kind, ID: in.Link.ID}
				}
				return change(svc().Update(withModules(ctx, d), commitmentsapp.UpdateInput{Ref: in.Ref, Title: in.Title, Notes: in.Notes, Due: in.Due, RemindAt: in.RemindAt,
					Priority: in.Priority, Link: link, Clear: in.Clear, UpdatedTS: in.UpdatedTS}))
			}),
		api.Command(d, describeCommitment(commitmentsapp.DeleteName), application.APIShape{Area: area, Body: application.BodyNone, Description: "Delete a task by `ref`. " + commitmentReadOnly + commitmentTopics,
			Errors: []application.Code{application.Invalid, application.NotFound, application.Conflict}},
			"DELETE /commitments/{ref}", func(ctx context.Context, in CommitmentRefRequest) (application.Change[CommitmentChanged], error) {
				return change(svc().Delete(withModules(ctx, d), commitmentsapp.RefInput{Ref: in.Ref}))
			}),
	}
}

type CommitmentSnoozeRequest struct {
	Ref   string  `path:"ref" json:"-"`
	Until float64 `json:"until"`
}

func commitmentSnoozeRoute(d *web.Deps, svc func() *commitmentsapp.Service) application.Binding {
	return api.Command(d, describeCommitment(commitmentsapp.SnoozeName), application.APIShape{Area: "tasks", Description: "Snooze an open task or commitment by `ref` until `until` (Unix seconds, in the future). The item leaves `/queue` until then and shows `snoozed_until` in `/commitments`. Undoable. " + commitmentTopics,
		Errors: []application.Code{application.Invalid, application.NotFound, application.Conflict}},
		"POST /commitments/{ref}/snooze", func(ctx context.Context, in CommitmentSnoozeRequest) (application.Change[CommitmentChanged], error) {
			c, err := svc().Snooze(withModules(ctx, d), commitmentsapp.SnoozeInput{Ref: in.Ref, Until: in.Until})
			if err != nil {
				return application.Change[CommitmentChanged]{}, err
			}
			return application.Change[CommitmentChanged]{Data: CommitmentChanged{Ref: c.Data.Ref, Status: c.Data.Status, Item: c.Data.Item}, EffectIDs: c.EffectIDs, AuditID: c.AuditID}, nil
		})
}
