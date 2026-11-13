package application

import (
	"context"
	"encoding/json"
	"math/bits"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/workspace"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/textsearch"
)

const (
	ThreadListName       application.Name = "assistant.thread.list"
	ThreadTranscriptName application.Name = "assistant.thread.transcript"
	ThreadWorkName       application.Name = "assistant.thread.work"
	ThreadHistoryName    application.Name = "assistant.thread.history"
	RunEventsName        application.Name = "assistant.run.events"
	RunDetailName        application.Name = "assistant.run.detail"
	unknownThread                         = "unknown thread"
	unknownRun                            = "unknown run"
	unknownRange                          = "unknown range"
)

var transcriptScopes = []string{state.Assistant, state.Review, state.Effects}

type Work struct {
	Needs     []Need             `json:"needs"`
	Plan      *workspace.Plan    `json:"plan"`
	Sources   []workspace.Source `json:"sources"`
	Subagents []subagents.Card   `json:"subagents"`
	Files     []files.Row        `json:"files"`
	Drafts    []map[string]any   `json:"drafts"`
	Code      []any              `json:"code"`
}

type Need struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

type Transcript struct {
	ModelLabel      string          `json:"model_label"`
	Thread          threads.Thread  `json:"thread"`
	Items           []*service.Item `json:"items"`
	Runs            []journal.Run   `json:"runs"`
	Active          *journal.Run    `json:"active"`
	Queued          []Queued        `json:"queued"`
	Cost            float64         `json:"cost"`
	Meter           service.Meter   `json:"meter"`
	LastOperatorSeq *int64          `json:"last_operator_seq"`
	BranchedFrom    *threads.Thread `json:"branched_from"`
	Question        *Question       `json:"question"`
	History         bool            `json:"-"`
	Earlier         int64           `json:"-"`
}

type Question struct {
	CallID    int64  `json:"call_id"`
	RunID     int64  `json:"run_id"`
	ToolUseID string `json:"tool_use_id"`
	Text      string `json:"text"`
}

type Queued struct {
	ID         int64    `json:"id"`
	ThreadID   int64    `json:"thread_id"`
	Text       string   `json:"text"`
	CreatedAt  float64  `json:"created_at"`
	ConsumedAt *float64 `json:"consumed_at"`
	RunID      *int64   `json:"run_id"`
	FilesJSON  *string  `json:"files_json"`
	Files      []any    `json:"files"`
}

type ThreadInput struct {
	ID int64
}

type TranscriptInput struct {
	ID     int64
	Limit  int
	Before int64
}

type ThreadListInput struct {
	Q         string
	Archived  bool
	ProjectID int64
	Limit     int
	After     *threads.ListKey
	Strict    bool
}

type ThreadList struct {
	Items []threads.Listed `json:"items"`
	Next  *threads.ListKey `json:"-"`
}

type HistoryInput struct {
	ThreadID int64
	Start    int64
	N        int64
}

type HistoryLine struct {
	ThreadID int64
	Text     string
	From     int64
	To       int64
	N        int64
	State    string
	Leaf     bool
}

type History struct {
	Lines []HistoryLine
	Leaf  *string
}

type RunInput struct {
	ThreadID int64
	RunID    int64
	After    int64
}

type EventStream struct {
	RunID int64
	After int64
	Run   journal.Run
}

func describeRead[I, O any](name application.Name) application.Definition {
	return application.Describe[I, O](name, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeThreadList() application.Definition {
	return describeRead[ThreadListInput, ThreadList](ThreadListName)
}

func DescribeThreadTranscript() application.Definition {
	return describeRead[TranscriptInput, Transcript](ThreadTranscriptName)
}

func DescribeThreadWork() application.Definition {
	return describeRead[ThreadInput, Work](ThreadWorkName)
}

func DescribeThreadHistory() application.Definition {
	return describeRead[HistoryInput, History](ThreadHistoryName)
}

func DescribeRunEvents() application.Definition {
	return describeRead[RunInput, EventStream](RunEventsName)
}

func DescribeRunDetail() application.Definition {
	return describeRead[RunInput, journal.Run](RunDetailName)
}

func listThreads(ctx context.Context, q db.Querier, in ThreadListInput) (ThreadList, error) {
	match := ""
	if in.Q != "" {
		var err error
		if match, err = textsearch.FTSQuery(in.Q); err != nil {
			return ThreadList{}, invalid("q", err.Error(), err)
		}
		if match == "" && in.Strict {
			return ThreadList{Items: []threads.Listed{}}, nil
		}
	}
	found, err := threads.ListThreads(ctx, q, threads.ListOpts{Match: match, Limit: in.Limit + 1, Archived: in.Archived, ProjectID: in.ProjectID, After: in.After})
	if err != nil {
		return ThreadList{}, err
	}
	out := ThreadList{Items: found}
	if len(found) > in.Limit {
		out.Items = found[:in.Limit]
		key := out.Items[in.Limit-1].Key()
		out.Next = &key
	}
	if out.Items == nil {
		out.Items = []threads.Listed{}
	}
	ids := make([]int64, len(out.Items))
	for i := range out.Items {
		ids[i] = out.Items[i].ID
	}
	costs, err := inference.ThreadCosts(ctx, q, ids)
	if err != nil {
		return ThreadList{}, err
	}
	for i := range out.Items {
		out.Items[i].Cost = costs[out.Items[i].ID]
	}
	return out, nil
}

func (s *Service) ThreadList(ctx context.Context, in ThreadListInput) (application.Read[ThreadList], error) {
	var out ThreadList
	rev, err := s.snapshot(ctx, string(ThreadListName), []string{state.Assistant}, in, func(q db.Querier) error {
		var err error
		out, err = listThreads(ctx, q, in)
		return err
	})
	return application.Read[ThreadList]{Revision: rev, Data: out}, err
}

func decodeBlocks(blocks []json.RawMessage) ([]any, error) {
	out := make([]any, len(blocks))
	for i, raw := range blocks {
		v, err := jsonx.Loads(raw)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func transcriptOf(v *service.ThreadView) (Transcript, error) {
	items := make([]*service.Item, len(v.Items))
	copy(items, v.Items)
	queued := make([]Queued, len(v.Queued))
	for i, m := range v.Queued {
		files, err := decodeBlocks(m.Files)
		if err != nil {
			return Transcript{}, err
		}
		queued[i] = Queued{ID: m.ID, ThreadID: m.ThreadID, Text: m.Text, CreatedAt: m.CreatedAt, ConsumedAt: m.ConsumedAt,
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
	var question *Question
	if v.Question != nil {
		question = &Question{CallID: v.Question.CallID, RunID: v.Question.RunID, ToolUseID: v.Question.ToolUseID, Text: v.Question.Text}
	}
	return Transcript{ModelLabel: label, Thread: v.Thread, Items: items, Runs: runs, Active: v.Active, Queued: queued,
		Cost: v.Cost, Meter: v.Meter, LastOperatorSeq: v.LastOperatorSeq, BranchedFrom: v.BranchedFrom, Question: question}, nil
}

func hasHistory(ctx context.Context, q db.Querier, threadID int64) bool {
	var n int
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND state IN (?, ?)", threadID, memory.Summary, memory.Fallback).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

func transcript(ctx context.Context, q db.Querier, in TranscriptInput) (Transcript, error) {
	v, err := service.View(ctx, q, in.ID)
	if err != nil {
		return Transcript{}, err
	}
	if v == nil {
		return Transcript{}, notFound(unknownThread)
	}
	out, err := transcriptOf(v)
	if err != nil {
		return Transcript{}, err
	}
	if in.Before > 0 {
		end := len(out.Items)
		for end > 0 && out.Items[end-1].Seq >= in.Before {
			end--
		}
		out.Items = out.Items[:end]
	}
	if in.Limit > 0 && len(out.Items) > in.Limit {
		out.Items = out.Items[len(out.Items)-in.Limit:]
		out.Earlier = out.Items[0].Seq
	}
	out.History = hasHistory(ctx, q, in.ID)
	return out, nil
}

func (s *Service) Transcript(ctx context.Context, in TranscriptInput) (application.Read[Transcript], error) {
	var out Transcript
	rev, err := s.snapshot(ctx, string(ThreadTranscriptName), transcriptScopes, in, func(q db.Querier) error {
		var err error
		out, err = transcript(ctx, q, in)
		return err
	})
	return application.Read[Transcript]{Revision: rev, Data: out}, err
}

func work(ctx context.Context, q db.Querier, threadID int64, now float64) (Work, error) {
	children, err := workspace.ChildThreads(ctx, q, threadID)
	if err != nil {
		return Work{}, err
	}
	w := Work{Subagents: []subagents.Card{}, Code: []any{}}
	if w.Plan, err = workspace.PlanOf(ctx, q, threadID); err != nil {
		return Work{}, err
	}
	if w.Sources, err = workspace.SourcesWith(ctx, q, threadID, children); err != nil {
		return Work{}, err
	}
	for _, child := range children {
		card, err := subagents.ChildCard(ctx, q, child, now)
		if err != nil {
			return Work{}, err
		}
		w.Subagents = append(w.Subagents, card)
	}
	if w.Files, err = files.OfThread(ctx, q, threadID); err != nil {
		return Work{}, err
	}
	if w.Drafts, err = service.DraftsOf(ctx, q, threadID); err != nil {
		return Work{}, err
	}
	pending, err := proposals.PendingInThread(ctx, q, threadID)
	if err != nil {
		return Work{}, err
	}
	w.Needs = make([]Need, len(pending))
	for i, p := range pending {
		w.Needs[i] = Need{ID: p.ID, Title: p.Title}
	}
	if w.Sources == nil {
		w.Sources = []workspace.Source{}
	}
	if w.Files == nil {
		w.Files = []files.Row{}
	}
	return w, nil
}

func (s *Service) Work(ctx context.Context, in ThreadInput) (application.Read[Work], error) {
	var out Work
	now := s.now()
	rev, err := s.snapshot(ctx, string(ThreadWorkName), transcriptScopes, struct {
		ThreadInput
		Minute int64
	}{in, int64(now) / 60}, func(q db.Querier) error {
		var err error
		out, err = work(ctx, q, in.ID, now)
		return err
	})
	return application.Read[Work]{Revision: rev, Data: out}, err
}

func (s *Service) History(ctx context.Context, in HistoryInput) (application.Read[History], error) {
	if in.N == 0 {
		v, err := memory.Build(ctx, s.db.Write, in.ThreadID, s.now())
		if err != nil || v == nil {
			return application.Read[History]{}, notFound(unknownThread)
		}
		out := History{Lines: make([]HistoryLine, len(v.Lines))}
		for i, l := range v.Lines {
			out.Lines[i] = HistoryLine{ThreadID: in.ThreadID, Text: l.Text, From: l.From, To: l.To, N: 1 << l.Level, State: l.State, Leaf: l.Level == 0}
		}
		return application.Read[History]{Data: out}, nil
	}
	var out History
	rev, err := s.snapshot(ctx, string(ThreadHistoryName), []string{state.Assistant}, in, func(q db.Querier) error {
		var err error
		out, err = zoom(ctx, q, in)
		return err
	})
	return application.Read[History]{Revision: rev, Data: out}, err
}

func zoom(ctx context.Context, q db.Querier, in HistoryInput) (History, error) {
	n, start := in.N, in.Start
	if n < 1 || n&(n-1) != 0 {
		return History{}, notFound(unknownRange)
	}
	nodes, err := memory.Tree(ctx, q, in.ThreadID)
	if err != nil {
		return History{}, notFound(unknownRange)
	}
	if n == 1 {
		text, ok := leafText(ctx, q, in.ThreadID, start, nodes)
		if !ok {
			return History{}, notFound(unknownRange)
		}
		return History{Leaf: &text}, nil
	}
	level := bits.TrailingZeros64(uint64(n))
	var parent *memory.Node
	for i := range nodes {
		if nodes[i].Level == level && nodes[i].FromSeq == start && nodes[i].ToSeq == start+n-1 {
			parent = &nodes[i]
			break
		}
	}
	if parent == nil {
		return History{}, notFound(unknownRange)
	}
	var children []HistoryLine
	for i := range nodes {
		if nodes[i].Level == level-1 && (nodes[i].Index == parent.Index*2 || nodes[i].Index == parent.Index*2+1) {
			children = append(children, HistoryLine{ThreadID: in.ThreadID, Text: nodes[i].Text, From: nodes[i].FromSeq, To: nodes[i].ToSeq,
				N: n / 2, State: nodes[i].State, Leaf: level-1 == 0})
		}
	}
	if len(children) != 2 {
		return History{}, notFound(unknownRange)
	}
	return History{Lines: children}, nil
}

func runOf(ctx context.Context, q db.Querier, in RunInput) (journal.Run, error) {
	found, err := journal.GetRun(ctx, q, in.RunID)
	if err != nil {
		return journal.Run{}, err
	}
	if found == nil || found.ThreadID != in.ThreadID {
		return journal.Run{}, notFound(unknownRun)
	}
	return *found, nil
}

func (s *Service) RunDetail(ctx context.Context, in RunInput) (application.Read[journal.Run], error) {
	var out journal.Run
	rev, err := s.snapshot(ctx, string(RunDetailName), []string{state.Assistant}, in, func(q db.Querier) error {
		var err error
		out, err = runOf(ctx, q, in)
		return err
	})
	return application.Read[journal.Run]{Revision: rev, Data: out}, err
}

func (s *Service) RunEvents(ctx context.Context, in RunInput) (application.Read[EventStream], error) {
	var out EventStream
	rev, err := s.snapshot(ctx, string(RunEventsName), []string{state.Assistant}, in, func(q db.Querier) error {
		run, err := runOf(ctx, q, in)
		out = EventStream{RunID: in.RunID, After: in.After, Run: run}
		return err
	})
	return application.Read[EventStream]{Revision: rev, Data: out}, err
}

func leafText(ctx context.Context, q db.Querier, threadID, seq int64, nodes []memory.Node) (string, bool) {
	found := false
	for _, n := range nodes {
		if n.Level == 0 && n.FromSeq == seq {
			found = true
			break
		}
	}
	if !found {
		return "", false
	}
	messages, err := journal.MessagesOf(ctx, q, threadID)
	if err != nil {
		return "", false
	}
	for _, m := range messages {
		if m.Seq != seq {
			continue
		}
		blocks, err := block.DecodeAll(m.Content)
		if err != nil {
			return "", false
		}
		var parts []string
		for _, b := range blocks {
			if b.Kind == block.Text {
				parts = append(parts, b.Text())
			}
		}
		return strings.Join(parts, "\n"), true
	}
	return "", false
}
