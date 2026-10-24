package pages

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/assistant/workspace"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

type WorkView struct {
	Plan      *workspace.Plan    `json:"plan"`
	Sources   []workspace.Source `json:"sources"`
	Subagents []subagents.Card   `json:"subagents"`
	Files     []workspace.File   `json:"files"`
	Drafts    []map[string]any   `json:"drafts"`
	Code      []any              `json:"code"`
}

func workView(ctx context.Context, q db.Querier, threadID int64, now float64) (WorkView, error) {
	children, err := workspace.ChildThreads(ctx, q, threadID)
	if err != nil {
		return WorkView{}, err
	}
	w := WorkView{Subagents: []subagents.Card{}, Code: []any{}}
	if w.Plan, err = workspace.PlanOf(ctx, q, threadID); err != nil {
		return WorkView{}, err
	}
	if w.Sources, err = workspace.SourcesWith(ctx, q, threadID, children); err != nil {
		return WorkView{}, err
	}
	for _, child := range children {
		card, err := subagents.ChildCard(ctx, q, child, now)
		if err != nil {
			return WorkView{}, err
		}
		w.Subagents = append(w.Subagents, card)
	}
	if w.Files, err = workspace.FilesOf(ctx, q, threadID); err != nil {
		return WorkView{}, err
	}
	rows, err := service.DraftsOf(ctx, q, threadID)
	if err != nil {
		return WorkView{}, err
	}
	w.Drafts = make([]map[string]any, len(rows))
	for i, row := range rows {
		w.Drafts[i] = plainDict(row)
	}
	if w.Sources == nil {
		w.Sources = []workspace.Source{}
	}
	if w.Files == nil {
		w.Files = []workspace.File{}
	}
	return w, nil
}

func plainJSON(v any) any {
	switch x := v.(type) {
	case py.Dict:
		return plainDict(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = plainJSON(e)
		}
		return out
	}
	return v
}

func plainDict(d py.Dict) map[string]any {
	out := make(map[string]any, len(d))
	for _, pair := range d {
		out[pair.Key] = plainJSON(pair.Value)
	}
	return out
}

func decodeBlocks(blocks []json.RawMessage) ([]any, error) {
	out := make([]any, len(blocks))
	for i, raw := range blocks {
		v, err := py.JSONLoads(raw)
		if err != nil {
			return nil, err
		}
		out[i] = plainJSON(v)
	}
	return out, nil
}

func threadViewDict(v *service.ThreadView) (map[string]any, error) {
	items := make([]map[string]any, len(v.Items))
	for i, item := range v.Items {
		items[i] = itemDict(item)
	}
	queued := make([]map[string]any, len(v.Queued))
	for i, m := range v.Queued {
		files, err := decodeBlocks(m.Files)
		if err != nil {
			return nil, err
		}
		queued[i] = map[string]any{
			"id": m.ID, "thread_id": m.ThreadID, "text": m.Text, "created_at": m.CreatedAt, "consumed_at": m.ConsumedAt,
			"run_id": m.RunID, "files_json": m.FilesJSON, "files": files,
		}
	}
	runs := v.Runs
	if runs == nil {
		runs = []journal.Run{}
	}
	label := v.Thread.Model
	if m, err := pricing.Lookup(label); err == nil {
		label = m.Name
	}
	return map[string]any{
		"model_label": label, "thread": v.Thread, "items": items, "runs": runs, "active": v.Active, "queued": queued, "cost": v.Cost,
		"meter": v.Meter, "last_operator_seq": v.LastOperatorSeq, "branched_from": v.BranchedFrom,
	}, nil
}

func itemDict(item *service.Item) map[string]any {
	out := map[string]any{"seq": item.Seq, "run_id": item.RunID, "created_at": item.CreatedAt, "kind": item.Kind}
	switch item.Kind {
	case service.ItemUser:
		files := make([]map[string]any, len(item.Files))
		for i, f := range item.Files {
			files[i] = map[string]any{"id": f.ID, "name": f.Name, "media_type": f.MediaType, "kind": f.Kind}
		}
		out["text"], out["files"] = item.Text, files
	case service.ItemActivity:
		steps := make([]map[string]any, len(item.Steps))
		for i, s := range item.Steps {
			steps[i] = stepDict(s)
		}
		out["steps"] = steps
	case service.ItemAssistant:
		citations := make([]map[string]any, len(item.Citations))
		for i, c := range item.Citations {
			citations[i] = map[string]any{"url": c.URL, "title": c.Title, "cited": c.Cited}
		}
		out["text"], out["citations"] = item.Text, citations
	case service.ItemDraft:
		out["draft"] = plainDict(item.Draft)
	}
	return out
}

func stepDict(s *service.Step) map[string]any {
	switch s.Kind {
	case service.StepTool:
		return map[string]any{
			"kind": s.Kind, "id": s.ID, "name": s.Name, "args": s.Args, "state": s.State, "result": s.Result, "is_error": s.IsError,
			"child_thread_id": s.ChildThreadID,
		}
	case service.StepWebSearch:
		results := make([]map[string]any, len(s.Results))
		for i, r := range s.Results {
			results[i] = map[string]any{"url": r.URL, "title": r.Title}
		}
		return map[string]any{"kind": s.Kind, "results": results, "error": s.Error}
	}
	return map[string]any{"kind": s.Kind, "text": s.Text}
}
