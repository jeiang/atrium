package assistantmodule

import (
	"context"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/web"
)

type apiProjectFields struct {
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	Effort       string `json:"effort"`
	ContextLimit *int64 `json:"context_limit"`
	Private      bool   `json:"private"`
}

type apiProjectPatch struct {
	ProjectID    int64   `path:"project_id" api:"min=1" json:"-"`
	Name         *string `json:"name"`
	Instructions *string `json:"instructions"`
	Effort       *string `json:"effort"`
	ContextLimit *int64  `json:"context_limit"`
	Private      *bool   `json:"private"`
}

type apiProjectID struct {
	ProjectID int64 `path:"project_id" api:"min=1" json:"-"`
}
type apiProjectArchive struct {
	ProjectID int64 `path:"project_id" api:"min=1" json:"-"`
	Archived  bool  `json:"archived"`
}
type apiProjectList struct {
	Archived bool       `query:"archived" json:"-"`
	Limit    api.Limit  `query:"limit" json:"-"`
	Cursor   api.Cursor `query:"cursor" json:"-"`
}
type apiProjectChanged struct {
	ID int64 `json:"id"`
}

func (in apiProjectFields) fields() asstapp.ProjectFields {
	fields := asstapp.ProjectFields{Name: in.Name, Instructions: in.Instructions, Effort: in.Effort, Private: in.Private}
	if in.ContextLimit != nil {
		fields.ContextLimit = strconv.FormatInt(*in.ContextLimit, 10)
	}
	return fields
}

func cursorAfter(cursor api.Cursor) (int64, error) {
	if !cursor.Set() {
		return 0, nil
	}
	var after int64
	if err := cursor.Last(&after); err != nil {
		return 0, err
	}
	if after <= 0 {
		return 0, &application.Error{Code: application.BadCursor, Message: "The cursor is not valid."}
	}
	return after, nil
}

func projectAPIBindings(p *apiPages) []application.Binding {
	shape := func() application.APIShape {
		return application.APIShape{Area: "projects", Errors: errs(application.Invalid, application.NotFound, application.Conflict)}
	}
	changed := func(value asstapp.ProjectChanged) apiProjectChanged { return apiProjectChanged{ID: value.ID} }
	create := shape()
	create.Success = 201
	return []application.Binding{
		apiQuery(p, asstapp.DescribeProjectList(), shape(), "GET /projects", func(ctx context.Context, svc *asstapp.Service, in apiProjectList) (application.Read[web.List[asstapp.Project]], error) {
			after, err := cursorAfter(in.Cursor)
			if err != nil {
				return application.Read[web.List[asstapp.Project]]{}, err
			}
			read, err := svc.ProjectList(ctx, asstapp.ProjectListInput{Archived: in.Archived, After: after, Limit: int(in.Limit) + 1})
			if err != nil {
				return application.Read[web.List[asstapp.Project]]{}, err
			}
			out := web.List[asstapp.Project]{Items: read.Data}
			if len(out.Items) > int(in.Limit) {
				out.Items = out.Items[:int(in.Limit)]
				out.NextCursor = in.Cursor.Next(out.Items[len(out.Items)-1].ID)
			}
			return application.Read[web.List[asstapp.Project]]{Revision: read.Revision, Data: out}, nil
		}),
		apiQuery(p, asstapp.DescribeProjectDetail(), shape(), "GET /projects/{project_id}", func(ctx context.Context, svc *asstapp.Service, in apiProjectID) (application.Read[asstapp.ProjectDetail], error) {
			return svc.ProjectDetail(ctx, asstapp.ProjectInput{ID: in.ProjectID})
		}),
		apiCommand(p, asstapp.DescribeProjectCreate(), create, "POST /projects", func(ctx context.Context, svc *asstapp.Service, in apiProjectFields) (application.Change[apiProjectChanged], error) {
			if strings.TrimSpace(in.Name) == "" {
				return application.Change[apiProjectChanged]{}, invalidField("name", "Give the project a name.")
			}
			result, err := svc.ProjectCreate(ctx, in.fields())
			return changeAs(result, err, changed)
		}),
		apiCommand(p, asstapp.DescribeProjectEdit(), shape(), "PATCH /projects/{project_id}", func(ctx context.Context, svc *asstapp.Service, in apiProjectPatch) (application.Change[apiProjectChanged], error) {
			read, err := svc.ProjectDetail(ctx, asstapp.ProjectInput{ID: in.ProjectID})
			if err != nil {
				return application.Change[apiProjectChanged]{}, err
			}
			current := read.Data.Project
			fields := asstapp.ProjectFields{Name: current.Name, Instructions: current.Instructions, Effort: current.Effort, ContextLimit: strconv.FormatInt(current.ContextLimit, 10), Private: current.Private}
			if in.Name != nil {
				fields.Name = *in.Name
			}
			if in.Instructions != nil {
				fields.Instructions = *in.Instructions
			}
			if in.Effort != nil {
				fields.Effort = *in.Effort
			}
			if in.ContextLimit != nil {
				fields.ContextLimit = strconv.FormatInt(*in.ContextLimit, 10)
			}
			if in.Private != nil {
				fields.Private = *in.Private
			}
			if strings.TrimSpace(fields.Name) == "" {
				return application.Change[apiProjectChanged]{}, invalidField("name", "Give the project a name.")
			}
			result, err := svc.ProjectEdit(ctx, asstapp.ProjectEditInput{ID: in.ProjectID, ProjectFields: fields})
			return changeAs(result, err, changed)
		}),
		apiCommand(p, asstapp.DescribeProjectArchive(), shape(), "POST /projects/{project_id}/archive", func(ctx context.Context, svc *asstapp.Service, in apiProjectArchive) (application.Change[apiProjectChanged], error) {
			result, err := svc.ProjectArchive(ctx, asstapp.ProjectArchiveInput{ID: in.ProjectID, Archived: in.Archived})
			return changeAs(result, err, changed)
		}),
	}
}
