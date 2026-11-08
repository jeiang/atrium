package pages

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/workspace"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

type WorkView struct {
	Plan      *workspace.Plan    `json:"plan"`
	Sources   []workspace.Source `json:"sources"`
	Subagents []subagents.Card   `json:"subagents"`
	Files     []files.Row        `json:"files"`
	Drafts    []py.Dict          `json:"drafts"`
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
	if w.Files, err = files.OfThread(ctx, q, threadID); err != nil {
		return WorkView{}, err
	}
	rows, err := service.DraftsOf(ctx, q, threadID)
	if err != nil {
		return WorkView{}, err
	}
	w.Drafts = rows
	if w.Sources == nil {
		w.Sources = []workspace.Source{}
	}
	if w.Files == nil {
		w.Files = []files.Row{}
	}
	return w, nil
}

func decodeBlocks(blocks []json.RawMessage) ([]any, error) {
	out := make([]any, len(blocks))
	for i, raw := range blocks {
		v, err := py.JSONLoads(raw)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

type ThreadData struct {
	ModelLabel      string          `json:"model_label"`
	Thread          threads.Thread  `json:"thread"`
	Items           []*service.Item `json:"items"`
	Runs            []journal.Run   `json:"runs"`
	Active          *journal.Run    `json:"active"`
	Queued          []QueuedView    `json:"queued"`
	Cost            float64         `json:"cost"`
	Meter           service.Meter   `json:"meter"`
	LastOperatorSeq *int64          `json:"last_operator_seq"`
	BranchedFrom    *threads.Thread `json:"branched_from"`
	Question        *QuestionView   `json:"question"`
}

type QuestionView struct {
	CallID    int64  `json:"call_id"`
	RunID     int64  `json:"run_id"`
	ToolUseID string `json:"tool_use_id"`
	Text      string `json:"text"`
}

type QueuedView struct {
	ID         int64    `json:"id"`
	ThreadID   int64    `json:"thread_id"`
	Text       string   `json:"text"`
	CreatedAt  float64  `json:"created_at"`
	ConsumedAt *float64 `json:"consumed_at"`
	RunID      *int64   `json:"run_id"`
	FilesJSON  *string  `json:"files_json"`
	Files      []any    `json:"files"`
}

type ThreadPage struct {
	View     *ThreadData        `json:"view"`
	ThreadID int64              `json:"thread_id"`
	History  bool               `json:"history"`
	Composer ComposerView       `json:"composer"`
	Work     WorkView           `json:"work"`
	Projects []projects.Project `json:"projects"`
	Project  *projects.Project  `json:"project"`
}

type QueuedData struct {
	Queued []QueuedView `json:"queued"`
}

type FilePreview struct {
	ID        int64      `json:"id"`
	ThreadID  *int64     `json:"thread_id"`
	RunID     *int64     `json:"run_id"`
	Name      string     `json:"name"`
	MediaType string     `json:"media_type"`
	Bytes     int64      `json:"bytes"`
	Path      string     `json:"path"`
	Origin    string     `json:"origin"`
	CreatedAt float64    `json:"created_at"`
	Text      *string    `json:"text"`
	Tokens    int64      `json:"tokens"`
	ProjectID *int64     `json:"project_id"`
	Render    string     `json:"render"`
	Body      string     `json:"body"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated"`
}

type FileData struct {
	File    FilePreview `json:"file"`
	FileURL string      `json:"file_url"`
}

type ProjectFiles struct {
	Project *projects.Project `json:"project"`
	Files   []files.Row       `json:"files"`
	Error   *string           `json:"error"`
}

type OptionsSaved struct {
	View     *ThreadData  `json:"view"`
	Composer ComposerView `json:"composer"`
}

type RunStarted struct {
	View     *ThreadData     `json:"view"`
	ThreadID int64           `json:"thread_id"`
	RunID    int64           `json:"run_id"`
	Items    []*service.Item `json:"items"`
	After    int64           `json:"after"`
}

type RunLive struct {
	View     *ThreadData `json:"view"`
	ThreadID int64       `json:"thread_id"`
	RunID    int64       `json:"run_id"`
	After    int64       `json:"after"`
}

type RunDone struct {
	View     *ThreadData     `json:"view"`
	ThreadID int64           `json:"thread_id"`
	Run      *journal.Run    `json:"run"`
	Items    []*service.Item `json:"items"`
}

func threadView(v *service.ThreadView) (*ThreadData, error) {
	items := make([]*service.Item, len(v.Items))
	copy(items, v.Items)
	queued := make([]QueuedView, len(v.Queued))
	for i, m := range v.Queued {
		files, err := decodeBlocks(m.Files)
		if err != nil {
			return nil, err
		}
		queued[i] = QueuedView{ID: m.ID, ThreadID: m.ThreadID, Text: m.Text, CreatedAt: m.CreatedAt, ConsumedAt: m.ConsumedAt,
			RunID: m.RunID, FilesJSON: m.FilesJSON, Files: files}
	}
	runs := v.Runs
	if runs == nil {
		runs = []journal.Run{}
	}
	label := v.Thread.Model
	if m, err := pricing.Lookup(label); err == nil {
		label = m.Name
	}
	var question *QuestionView
	if v.Question != nil {
		question = &QuestionView{CallID: v.Question.CallID, RunID: v.Question.RunID, ToolUseID: v.Question.ToolUseID, Text: v.Question.Text}
	}
	return &ThreadData{ModelLabel: label, Thread: v.Thread, Items: items, Runs: runs, Active: v.Active, Queued: queued,
		Cost: v.Cost, Meter: v.Meter, LastOperatorSeq: v.LastOperatorSeq, BranchedFrom: v.BranchedFrom, Question: question}, nil
}
