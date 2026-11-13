package assistantmodule

import (
	"context"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/attention"
	attentionapp "github.com/jeiang/atrium/internal/attention/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

const searchQueryMax = 200

type apiSearchRequest struct {
	Q         string     `query:"q" json:"-"`
	ProjectID int64      `query:"project_id" api:"min=1" json:"-"`
	View      string     `query:"view" api:"enum=archived" json:"-"`
	Limit     api.Limit  `query:"limit" json:"-"`
	Cursor    api.Cursor `query:"cursor" json:"-"`
}

type apiSearchHit struct {
	Type   string          `json:"type"`
	Thread apiListedThread `json:"thread"`
}

type apiAttentionItem struct {
	Kind     string          `json:"kind"`
	SourceID string          `json:"source_id"`
	Title    string          `json:"title"`
	Detail   string          `json:"detail"`
	Due      float64         `json:"due,omitempty"`
	Created  float64         `json:"created,omitempty"`
	Group    attention.Group `json:"group"`
	Actions  []string        `json:"actions"`
}

type apiAttentionGroup struct {
	Group attention.Group `json:"group"`
	Label string          `json:"label"`
	Count int             `json:"count"`
}

type apiAttention struct {
	Items  []apiAttentionItem  `json:"items"`
	Groups []apiAttentionGroup `json:"groups"`
}

func listedOut(t threads.Listed) apiListedThread {
	return apiListedThread{
		ID: t.ID, Title: t.Title, Pinned: bool(t.Pinned), Archived: bool(t.Archived), ProjectID: t.ProjectID,
		ProjectName: t.ProjectName, RunState: t.RunState, CostUSD: t.Cost, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func attentionOut(r attention.Result) apiAttention {
	out := apiAttention{Items: []apiAttentionItem{}, Groups: []apiAttentionGroup{}}
	for _, item := range r.Items {
		actions := []string{}
		for _, a := range item.Actions {
			actions = append(actions, a.Kind)
		}
		out.Items = append(out.Items, apiAttentionItem{Kind: item.Kind, SourceID: item.SourceID, Title: item.Title, Detail: item.Detail,
			Due: item.Due, Created: item.Created, Group: item.Group, Actions: actions})
	}
	for _, g := range r.Groups {
		out.Groups = append(out.Groups, apiAttentionGroup{Group: g.Group, Label: g.Label, Count: g.Count})
	}
	return out
}

func searchAPIBindings(p *apiPages) []application.Binding {
	var d *web.Deps
	if p != nil {
		d = p.deps
	}
	return []application.Binding{
		apiQuery(p, asstapp.DescribeThreadList(), application.APIShape{Area: "search", Errors: errs(application.Invalid)}, "GET /search",
			func(ctx context.Context, svc *asstapp.Service, in apiSearchRequest) (application.Read[web.List[apiSearchHit]], error) {
				q := strings.TrimSpace(in.Q)
				if q == "" || utf8.RuneCountInString(q) > searchQueryMax {
					return application.Read[web.List[apiSearchHit]]{}, &application.Error{Code: application.Invalid, Message: "q must be 1 to 200 characters.",
						Fields: []application.Field{{Name: "q", Code: "invalid", Message: "Give a search of 1 to 200 characters."}}}
				}
				list := asstapp.ThreadListInput{Q: q, Limit: int(in.Limit), Archived: in.View == "archived", ProjectID: in.ProjectID, Strict: true}
				if in.Cursor.Set() {
					var key threads.ListKey
					if err := in.Cursor.Last(&key); err != nil {
						return application.Read[web.List[apiSearchHit]]{}, err
					}
					list.After = &key
				}
				read, err := svc.ThreadList(ctx, list)
				return readAs(read, err, func(v asstapp.ThreadList) web.List[apiSearchHit] {
					out := web.List[apiSearchHit]{Items: []apiSearchHit{}}
					if v.Next != nil {
						out.NextCursor = in.Cursor.Next(*v.Next)
					}
					for _, t := range v.Items {
						out.Items = append(out.Items, apiSearchHit{Type: "thread", Thread: listedOut(t)})
					}
					return out
				})
			}),
		api.Query(d, attentionapp.DescribeRead(), application.APIShape{Area: "review", Response: reflect.TypeFor[apiAttention]()}, "GET /attention",
			func(ctx context.Context, _ struct{}) (application.Read[apiAttention], error) {
				if module.SetFrom(ctx) == nil {
					ctx = module.WithSet(ctx, module.SetFrom(d.Ctx))
				}
				read, err := attentionapp.New(d.DB, assistant.Now).Read(ctx, attentionapp.ReadInput{})
				return readAs(read, err, attentionOut)
			}),
	}
}
