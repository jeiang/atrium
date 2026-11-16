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

type apiSpaceFields struct {
	Name         string `json:"name"`
	Instructions string `json:"instructions,omitempty"`
	Effort       string `json:"effort"`
	ContextLimit *int64 `json:"context_limit"`
	Private      bool   `json:"private,omitempty"`
}

type apiSpacePatch struct {
	SpaceID      int64   `path:"space_id" api:"min=1" json:"-"`
	Name         *string `json:"name"`
	Instructions *string `json:"instructions"`
	Effort       *string `json:"effort"`
	ContextLimit *int64  `json:"context_limit"`
	Private      *bool   `json:"private"`
}

type apiSpaceID struct {
	SpaceID int64 `path:"space_id" api:"min=1" json:"-"`
}
type apiSpaceArchive struct {
	SpaceID  int64 `path:"space_id" api:"min=1" json:"-"`
	Archived bool  `json:"archived"`
}
type apiSpaceList struct {
	Archived bool       `query:"archived" json:"-"`
	Limit    api.Limit  `query:"limit" json:"-"`
	Cursor   api.Cursor `query:"cursor" json:"-"`
}
type apiSpaceChanged struct {
	ID int64 `json:"id"`
}

type SpacePage struct {
	Space    asstapp.Space         `json:"space"`
	Sections []KnowledgeSection    `json:"sections"`
	Files    []asstapp.SpaceFile   `json:"files"`
	Threads  []asstapp.SpaceThread `json:"threads"`
	Tasks    []asstapp.SpaceTask   `json:"tasks"`
	Records  []asstapp.SpaceRecord `json:"records"`
	Counts   asstapp.SpaceCounts   `json:"counts"`
}

func spacePage(d asstapp.SpaceDetail) SpacePage {
	return SpacePage{Space: d.Space, Sections: knowledgeSections(d.Sections), Files: d.Files, Threads: d.Threads, Tasks: d.Tasks,
		Records: d.Records, Counts: d.Counts}
}

func (in apiSpaceFields) fields() asstapp.SpaceFields {
	fields := asstapp.SpaceFields{Name: in.Name, Instructions: in.Instructions, Effort: in.Effort, Private: in.Private}
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
		return 0, api.ErrBadCursor
	}
	return after, nil
}

func spaceAPIBindings(p *apiPages) []application.Binding {
	shape := func() application.APIShape {
		return application.APIShape{Area: "knowledge", Errors: errs(application.Invalid, application.NotFound, application.Conflict)}
	}
	changed := func(value asstapp.SpaceChanged) apiSpaceChanged { return apiSpaceChanged{ID: value.ID} }
	create := shape()
	create.Success = 201
	return []application.Binding{
		apiQuery(p, asstapp.DescribeSpaceList(), shape(), "GET /knowledge/spaces", func(ctx context.Context, svc *asstapp.Service, in apiSpaceList) (application.Read[web.List[asstapp.Space]], error) {
			after, err := cursorAfter(in.Cursor)
			if err != nil {
				return application.Read[web.List[asstapp.Space]]{}, err
			}
			read, err := svc.SpaceList(ctx, asstapp.SpaceListInput{Archived: in.Archived, After: after, Limit: int(in.Limit) + 1})
			if err != nil {
				return application.Read[web.List[asstapp.Space]]{}, err
			}
			out := web.List[asstapp.Space]{Items: read.Data}
			if len(out.Items) > int(in.Limit) {
				out.Items = out.Items[:int(in.Limit)]
				out.NextCursor = in.Cursor.Next(out.Items[len(out.Items)-1].ID)
			}
			return application.Read[web.List[asstapp.Space]]{Revision: read.Revision, Data: out}, nil
		}),
		apiQuery(p, asstapp.DescribeSpaceDetail(), shape(), "GET /knowledge/spaces/{space_id}", func(ctx context.Context, svc *asstapp.Service, in apiSpaceID) (application.Read[SpacePage], error) {
			read, err := svc.SpaceDetail(ctx, asstapp.SpaceInput{ID: in.SpaceID})
			return readAs(read, err, spacePage)
		}),
		apiCommand(p, asstapp.DescribeSpaceCreate(), create, "POST /knowledge/spaces", func(ctx context.Context, svc *asstapp.Service, in apiSpaceFields) (application.Change[apiSpaceChanged], error) {
			if strings.TrimSpace(in.Name) == "" {
				return application.Change[apiSpaceChanged]{}, invalidField("name", "Give the space a name.")
			}
			result, err := svc.SpaceCreate(ctx, in.fields())
			return changeAs(result, err, changed)
		}),
		apiCommand(p, asstapp.DescribeSpaceEdit(), shape(), "PATCH /knowledge/spaces/{space_id}", func(ctx context.Context, svc *asstapp.Service, in apiSpacePatch) (application.Change[apiSpaceChanged], error) {
			read, err := svc.SpaceDetail(ctx, asstapp.SpaceInput{ID: in.SpaceID})
			if err != nil {
				return application.Change[apiSpaceChanged]{}, err
			}
			current := read.Data.Space
			fields := asstapp.SpaceFields{Name: current.Name, Instructions: current.Instructions, Effort: current.Effort, ContextLimit: strconv.FormatInt(current.ContextLimit, 10), Private: current.Private}
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
				return application.Change[apiSpaceChanged]{}, invalidField("name", "Give the space a name.")
			}
			result, err := svc.SpaceEdit(ctx, asstapp.SpaceEditInput{ID: in.SpaceID, SpaceFields: fields})
			return changeAs(result, err, changed)
		}),
		apiCommand(p, asstapp.DescribeSpaceArchive(), shape(), "POST /knowledge/spaces/{space_id}/archive", func(ctx context.Context, svc *asstapp.Service, in apiSpaceArchive) (application.Change[apiSpaceChanged], error) {
			result, err := svc.SpaceArchive(ctx, asstapp.SpaceArchiveInput{ID: in.SpaceID, Archived: in.Archived})
			return changeAs(result, err, changed)
		}),
	}
}
