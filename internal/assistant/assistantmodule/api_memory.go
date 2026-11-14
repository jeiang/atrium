package assistantmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/web"
)

type apiMemoryThread struct {
	ThreadID int64 `path:"thread_id" api:"min=1" json:"-"`
}

type apiMemoryNode struct {
	ID        int64   `json:"id"`
	Level     int     `json:"level"`
	Index     int     `json:"index"`
	Text      string  `json:"text"`
	Bytes     int     `json:"bytes"`
	State     string  `json:"state"`
	FromSeq   int64   `json:"from_seq"`
	ToSeq     int64   `json:"to_seq"`
	ResultIDs []int64 `json:"result_ids"`
	CreatedAt float64 `json:"created_at"`
}

type apiMemoryTree struct {
	Nodes int             `json:"nodes"`
	Roots []apiMemoryNode `json:"roots"`
}

type apiMemoryView struct {
	Lines  []memory.Line `json:"lines"`
	Bytes  int           `json:"bytes"`
	Over   bool          `json:"over"`
	Target int           `json:"target"`
	High   int           `json:"high"`
}

type apiMemoryEstimate struct {
	Threads int     `json:"threads"`
	Nodes   int     `json:"nodes"`
	CostUSD float64 `json:"cost_usd"`
}

type apiMemoryWork struct {
	ThreadID *int64 `json:"thread_id"`
	Queued   bool   `json:"queued,omitempty"`
}

type apiMemoryBackfill struct {
	ThreadID *int64 `json:"thread_id"`
}

type apiSubagentList struct {
	Limit  api.Limit  `query:"limit" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
}

type apiSubagentName struct {
	Name string `path:"name" json:"-"`
}

type apiSubagentSet struct {
	Name        string   `path:"name" json:"-"`
	Description *string  `json:"description"`
	Prompt      *string  `json:"prompt"`
	Effort      *string  `json:"effort"`
	Tools       []string `json:"tools,omitempty"`
}

type apiSubagentEnabled struct {
	Name    string `path:"name" json:"-"`
	Enabled *bool  `json:"enabled" api:"required"`
}

func memoryTreeOut(tree asstapp.MemoryTree) apiMemoryTree {
	out := apiMemoryTree{Nodes: tree.Nodes, Roots: []apiMemoryNode{}}
	for _, n := range tree.Roots {
		out.Roots = append(out.Roots, apiMemoryNode{ID: n.ID, Level: n.Level, Index: n.Index, Text: n.Text, Bytes: n.Bytes, State: n.State,
			FromSeq: n.FromSeq, ToSeq: n.ToSeq, ResultIDs: append([]int64{}, n.ResultIDs...), CreatedAt: n.CreatedAt})
	}
	return out
}

func memoryAPIBindings(p *apiPages) []application.Binding {
	knowledge := func(codes ...application.Code) application.APIShape {
		return application.APIShape{Area: "knowledge", Errors: codes}
	}
	return []application.Binding{
		apiQuery(p, asstapp.DescribeMemoryTree(), knowledge(application.NotFound), "GET /memory/threads/{thread_id}/tree",
			func(ctx context.Context, svc *asstapp.Service, in apiMemoryThread) (application.Read[apiMemoryTree], error) {
				read, err := svc.MemoryTree(ctx, asstapp.MemoryThreadInput{ThreadID: in.ThreadID})
				return readAs(read, err, memoryTreeOut)
			}),
		apiQuery(p, asstapp.DescribeMemoryView(), knowledge(application.NotFound), "GET /memory/threads/{thread_id}",
			func(ctx context.Context, svc *asstapp.Service, in apiMemoryThread) (application.Read[apiMemoryView], error) {
				read, err := svc.MemoryView(ctx, asstapp.MemoryThreadInput{ThreadID: in.ThreadID})
				return readAs(read, err, func(v memory.View) apiMemoryView {
					lines := v.Lines
					if lines == nil {
						lines = []memory.Line{}
					}
					return apiMemoryView{Lines: lines, Bytes: v.Bytes, Over: v.Over, Target: v.Target, High: v.High}
				})
			}),
		apiQuery(p, asstapp.DescribeMemoryEstimate(), knowledge(), "GET /memory/estimate",
			func(ctx context.Context, svc *asstapp.Service, _ struct{}) (application.Read[apiMemoryEstimate], error) {
				read, err := svc.MemoryEstimate(ctx, asstapp.MemoryEstimateInput{})
				return readAs(read, err, func(e memory.Estimate) apiMemoryEstimate {
					return apiMemoryEstimate{Threads: e.Threads, Nodes: e.Nodes, CostUSD: e.CostUSD}
				})
			}),
		apiCommand(p, asstapp.DescribeMemoryWork(), knowledge(application.Invalid, application.NotFound, application.NotConfigured, application.Budget), "POST /memory/work",
			func(ctx context.Context, svc *asstapp.Service, in apiMemoryWork) (application.Change[asstapp.MemoryWorked], error) {
				if (in.ThreadID == nil) == !in.Queued {
					return application.Change[asstapp.MemoryWorked]{}, &application.Error{Code: application.Invalid, Message: "Give either thread_id or queued.",
						Fields: []application.Field{{Name: "thread_id", Code: "invalid", Message: "Give either thread_id or queued, not both."}}}
				}
				work := asstapp.MemoryWorkInput{Queued: in.Queued}
				if in.ThreadID != nil {
					if _, err := svc.MemoryTree(ctx, asstapp.MemoryThreadInput{ThreadID: *in.ThreadID}); err != nil {
						return application.Change[asstapp.MemoryWorked]{}, err
					}
					work.ThreadID = *in.ThreadID
				}
				return svc.MemoryWork(ctx, work)
			}),
		apiCommand(p, asstapp.DescribeMemoryBackfill(), knowledge(application.NotFound, application.Refused, application.NotConfigured, application.Budget), "POST /memory/backfill",
			func(ctx context.Context, svc *asstapp.Service, in apiMemoryBackfill) (application.Change[asstapp.MemoryWorked], error) {
				if in.ThreadID != nil {
					if _, err := svc.MemoryTree(ctx, asstapp.MemoryThreadInput{ThreadID: *in.ThreadID}); err != nil {
						return application.Change[asstapp.MemoryWorked]{}, err
					}
				}
				return svc.MemoryBackfill(ctx, asstapp.MemoryBackfillInput{ThreadID: in.ThreadID})
			}),
		apiQuery(p, asstapp.DescribeUsage(), application.APIShape{Area: "usage"}, "GET /usage",
			func(ctx context.Context, svc *asstapp.Service, _ struct{}) (application.Read[asstapp.Usage], error) {
				return svc.Usage(ctx, asstapp.UsageInput{})
			}),
	}
}

func subagentAPIBindings(p *apiPages) []application.Binding {
	settings := func(codes ...application.Code) application.APIShape {
		return application.APIShape{Area: "settings", Errors: codes}
	}
	remove := settings(application.NotFound, application.Refused)
	remove.Success = 204
	remove.Body = application.BodyNone
	tooled := func(svc *asstapp.Service) *asstapp.Service { return svc.WithTools(p.tools) }
	return []application.Binding{
		apiQuery(p, asstapp.DescribeSubagentList(), settings(application.Invalid), "GET /subagents",
			func(ctx context.Context, svc *asstapp.Service, in apiSubagentList) (application.Read[web.List[asstapp.Subagent]], error) {
				read, err := svc.SubagentList(ctx, asstapp.SubagentListInput{})
				if err != nil {
					return application.Read[web.List[asstapp.Subagent]]{}, err
				}
				page, err := pageAfter(read.Data, func(a asstapp.Subagent) string { return a.Name }, in.Cursor, in.Limit)
				return application.Read[web.List[asstapp.Subagent]]{Revision: read.Revision, Data: page}, err
			}),
		apiCommand(p, asstapp.DescribeSubagentSet(), settings(application.Invalid, application.Refused), "PUT /subagents/{name}",
			func(ctx context.Context, svc *asstapp.Service, in apiSubagentSet) (application.Change[asstapp.Subagent], error) {
				return tooled(svc).SubagentSet(ctx, asstapp.SubagentSetInput{Name: in.Name, Description: in.Description, Prompt: in.Prompt, Effort: in.Effort, Tools: in.Tools, CustomOnly: true})
			}),
		apiCommand(p, asstapp.DescribeSubagentEnabled(), settings(application.Invalid, application.NotFound, application.Refused), "PUT /subagents/{name}/enabled",
			func(ctx context.Context, svc *asstapp.Service, in apiSubagentEnabled) (application.Change[asstapp.Subagent], error) {
				if in.Enabled == nil {
					return application.Change[asstapp.Subagent]{}, invalidField("enabled", "Say whether the subagent is enabled.")
				}
				return svc.SubagentEnabled(ctx, asstapp.SubagentEnabledInput{Name: in.Name, Enabled: *in.Enabled})
			}),
		apiCommand(p, asstapp.DescribeSubagentDelete(), remove, "DELETE /subagents/{name}",
			func(ctx context.Context, svc *asstapp.Service, in apiSubagentName) (application.Change[asstapp.Subagent], error) {
				return svc.SubagentDelete(ctx, asstapp.SubagentDeleteInput{Name: in.Name})
			}),
	}
}
