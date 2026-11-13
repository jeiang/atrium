package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/anthropicconn"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	MemoryTreeName     application.Name = "assistant.memory.tree"
	MemoryViewName     application.Name = "assistant.memory.view"
	MemoryEstimateName application.Name = "assistant.memory.estimate"
	MemoryWorkName     application.Name = "assistant.memory.work"
	MemoryBackfillName application.Name = "assistant.memory.backfill"
	MemoryZoomName     application.Name = "assistant.memory.zoom"
	MemorySearchName   application.Name = "assistant.memory.search"
	ResultRecallName   application.Name = "assistant.result.recall"
)

var (
	memoryScopes = []string{state.Assistant}
	agentReaders = []application.Authority{application.Agent, application.Approved}
	RecallLimit  = tool.NewCeiling(assistant.TuningAgentRecallPageChars, 100, assistant.TuningAgentRecallMaxPageChars)
)

type MemoryThreadInput struct {
	ThreadID int64
}

type MemoryEstimateInput struct{}

type MemoryWorkInput struct {
	ThreadID int64
	Queued   bool
}

type MemoryBackfillInput struct {
	ThreadID *int64
}

type MemoryZoomInput struct {
	ThreadID int64
	ID       int64
	N        int64
	At       int64
}

type MemorySearchInput struct {
	ThreadID int64
	Query    string
	Project  int64
	Limit    int
}

type RecallInput struct {
	ThreadID int64
	ResultID int64
	Offset   int64
	Limit    int64
}

type MemoryTree struct {
	Nodes int           `json:"nodes"`
	Roots []memory.Node `json:"roots"`
}

type MemoryWorked struct {
	Threads    int `json:"threads"`
	Summarized int `json:"summarized"`
}

type ZoomChild struct {
	ID    int64  `json:"id"`
	N     int64  `json:"n"`
	To    int64  `json:"to"`
	State string `json:"state"`
	Text  string `json:"text"`
}

type MemoryZoom struct {
	ID       int64       `json:"id"`
	N        int64       `json:"n"`
	Children []ZoomChild `json:"children,omitempty"`
	ResultID int64       `json:"result_id,omitempty"`
	Page     *TextPage   `json:"page,omitempty"`
	Marks    tool.Marks  `json:"-"`
}

type TextPage struct {
	Offset int64  `json:"offset"`
	Total  int64  `json:"total_chars"`
	Text   string `json:"text"`
	Next   *int64 `json:"next_offset"`
}

type MemoryHit struct {
	memory.Hit
	Own bool
}

type MemorySearch struct {
	Hits  []MemoryHit `json:"hits"`
	Marks tool.Marks  `json:"-"`
}

type RecallPage struct {
	ResultID int64 `json:"result_id"`
	TextPage
}

func DescribeMemoryTree() application.Definition {
	return describeRead[MemoryThreadInput, MemoryTree](MemoryTreeName)
}

func DescribeMemoryView() application.Definition {
	return describeRead[MemoryThreadInput, memory.View](MemoryViewName)
}

func DescribeMemoryEstimate() application.Definition {
	return describeRead[MemoryEstimateInput, memory.Estimate](MemoryEstimateName)
}

func DescribeMemoryWork() application.Definition {
	return application.Describe[MemoryWorkInput, MemoryWorked](MemoryWorkName, application.Command,
		[]application.Authority{application.Operator, application.System}, application.External, true)
}

func DescribeMemoryBackfill() application.Definition {
	return application.Describe[MemoryBackfillInput, MemoryWorked](MemoryBackfillName, application.Command,
		[]application.Authority{application.Operator}, application.External, true)
}

func DescribeMemoryZoom() application.Definition {
	return application.Describe[MemoryZoomInput, MemoryZoom](MemoryZoomName, application.Query, agentReaders, application.NoEffect, false)
}

func DescribeMemorySearch() application.Definition {
	return application.Describe[MemorySearchInput, MemorySearch](MemorySearchName, application.Query, agentReaders, application.NoEffect, false)
}

func DescribeResultRecall() application.Definition {
	return application.Describe[RecallInput, RecallPage](ResultRecallName, application.Query, agentReaders, application.NoEffect, false)
}

func (s *Service) WithMemoryModel(resolve func(context.Context) (*anthropic.Client, error), inf *inference.Service) *Service {
	c := *s
	c.resolveClient, c.inference = resolve, inf
	return &c
}

func (s *Service) memoryClient(ctx context.Context) (*anthropic.Client, error) {
	if s.resolveClient != nil {
		return s.resolveClient(ctx)
	}
	lease, err := connections.OpenRead(ctx, s.db.Read, config.From(ctx).Host(), connections.ID(anthropicconn.Kind))
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	key, err := lease.Credential("api_key")
	if err != nil {
		return nil, config.NotConfigured("the Anthropic connection has no API key")
	}
	httpClient, err := lease.HTTP("url")
	if err != nil {
		return nil, err
	}
	return anthropic.New(strings.TrimSpace(key), anthropicconn.Env(s.env, lease), anthropic.HTTPClient(httpClient))
}

func (s *Service) memoryInference() *inference.Service {
	if s.inference != nil {
		return s.inference
	}
	return service.NewInference(s.db, s.now)
}

func rootsOf(nodes []memory.Node) []memory.Node {
	have := make(map[[2]int]bool, len(nodes))
	for _, n := range nodes {
		have[[2]int{n.Level, n.Index}] = true
	}
	var roots []memory.Node
	for _, n := range nodes {
		if have[[2]int{n.Level + 1, n.Index / 2}] {
			continue
		}
		roots = append(roots, n)
	}
	return roots
}

func (s *Service) MemoryTree(ctx context.Context, in MemoryThreadInput) (application.Read[MemoryTree], error) {
	var out MemoryTree
	rev, err := s.snapshot(ctx, string(MemoryTreeName), memoryScopes, in, func(q db.Querier) error {
		nodes, err := memory.Tree(ctx, q, in.ThreadID)
		out = MemoryTree{Nodes: len(nodes), Roots: rootsOf(nodes)}
		return err
	})
	return application.Read[MemoryTree]{Revision: rev, Data: out}, err
}

func (s *Service) MemoryView(ctx context.Context, in MemoryThreadInput) (application.Read[memory.View], error) {
	now := s.now()
	if err := memory.Rebuild(ctx, s.db.Write, in.ThreadID, now); err != nil {
		return application.Read[memory.View]{}, err
	}
	v, err := memory.Build(ctx, s.db.Write, in.ThreadID, now)
	if err != nil {
		return application.Read[memory.View]{}, err
	}
	if v == nil {
		return application.Read[memory.View]{}, notFound(fmt.Sprintf("unknown thread %d", in.ThreadID))
	}
	return application.Read[memory.View]{Data: *v}, nil
}

func (s *Service) MemoryEstimate(ctx context.Context, in MemoryEstimateInput) (application.Read[memory.Estimate], error) {
	var out memory.Estimate
	rev, err := s.snapshot(ctx, string(MemoryEstimateName), memoryScopes, in, func(q db.Querier) error {
		var err error
		out, err = memory.EstimateBackfill(ctx, q)
		return err
	})
	return application.Read[memory.Estimate]{Revision: rev, Data: out}, err
}

func (s *Service) memoryPass(ctx context.Context, name application.Name, tgt string, pass func(client *anthropic.Client, inf *inference.Service, model, effort string) (MemoryWorked, error)) (application.Change[MemoryWorked], error) {
	out, err := func() (MemoryWorked, error) {
		client, err := s.memoryClient(ctx)
		if err != nil {
			return MemoryWorked{}, err
		}
		model, err := memory.Model(ctx, s.db.Read)
		if err != nil {
			return MemoryWorked{}, err
		}
		effort, err := memory.Effort(ctx, s.db.Read)
		if err != nil {
			return MemoryWorked{}, err
		}
		return pass(client, s.memoryInference(), model, effort)
	}()
	if err != nil {
		var notConfigured config.NotConfigured
		if errors.As(err, &notConfigured) {
			err = &application.Error{Code: application.NotConfigured, Message: err.Error(), Cause: err}
		}
		var e *application.Error
		if errors.As(err, &e) {
			return application.Change[MemoryWorked]{}, s.reject(ctx, name, tgt, err)
		}
		if _, aerr := s.audit(ctx, nil, name, tgt, state.Failed); aerr != nil {
			return application.Change[MemoryWorked]{}, errors.Join(err, aerr)
		}
		return application.Change[MemoryWorked]{}, err
	}
	outcome := state.OK
	if out.Summarized == 0 {
		outcome = state.Noop
	}
	auditID, err := s.state.Audit(ctx, nil, state.Entry{Operation: string(name), Outcome: outcome, Target: tgt,
		DetailsJSON: fmt.Sprintf(`{"threads":%d,"summarized":%d}`, out.Threads, out.Summarized)})
	return application.Change[MemoryWorked]{Data: out, AuditID: auditID}, err
}

func (s *Service) MemoryWork(ctx context.Context, in MemoryWorkInput) (application.Change[MemoryWorked], error) {
	tgt := ""
	if !in.Queued {
		tgt = target("thread", in.ThreadID)
	}
	return s.memoryPass(ctx, MemoryWorkName, tgt, func(client *anthropic.Client, inf *inference.Service, model, effort string) (MemoryWorked, error) {
		found := []int64{in.ThreadID}
		if in.Queued {
			var err error
			if found, err = memory.QueuedThreads(ctx, s.db.Read, s.now()); err != nil {
				return MemoryWorked{}, err
			}
		}
		var out MemoryWorked
		for _, threadID := range found {
			n, err := memory.Work(ctx, s.db, threadID, memory.WorkOptions{
				Now: s.now(), Client: client, Inference: inf, Model: model, Effort: effort,
			})
			out.Threads++
			out.Summarized += n
			if err != nil {
				return out, err
			}
		}
		return out, nil
	})
}

func (s *Service) MemoryBackfill(ctx context.Context, in MemoryBackfillInput) (application.Change[MemoryWorked], error) {
	tgt := ""
	if in.ThreadID != nil {
		tgt = target("thread", *in.ThreadID)
	} else {
		done, err := memory.BackfillDone(ctx, s.db.Read)
		if err != nil {
			return application.Change[MemoryWorked]{}, err
		}
		if done != "" {
			return application.Change[MemoryWorked]{}, s.reject(ctx, MemoryBackfillName, tgt,
				refusal(fmt.Sprintf("backfill already completed at %s; use --thread to redo one thread", done)))
		}
	}
	at := s.now()
	return s.memoryPass(ctx, MemoryBackfillName, tgt, func(client *anthropic.Client, inf *inference.Service, model, effort string) (MemoryWorked, error) {
		n, err := memory.Backfill(ctx, s.db, in.ThreadID, client, inf, model, effort, at)
		return MemoryWorked{Summarized: n}, err
	})
}

func threadMarks(ctx context.Context, q db.Querier, ids []int64) (tool.Marks, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	list := make([]any, len(ids))
	for i, id := range ids {
		list[i] = id
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		return 0, err
	}
	rows, err := q.QueryContext(ctx, "SELECT marks FROM threads WHERE id IN (SELECT value FROM json_each(?))", encoded)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var out tool.Marks
	for rows.Next() {
		var marks int64
		if err := rows.Scan(&marks); err != nil {
			return 0, err
		}
		out |= tool.Marks(marks)
	}
	return out, rows.Err()
}

func runeSlice(s string, offset, limit int64) string {
	start, end := len(s), len(s)
	var n int64
	for i := range s {
		if n == offset {
			start = i
		}
		if n == offset+limit {
			end = i
			break
		}
		n++
	}
	if start > end {
		return ""
	}
	return s[start:end]
}

func pageOf(body string, offset, limit int64) TextPage {
	text := runeSlice(body, offset, limit)
	total := int64(utf8.RuneCountInString(body))
	end := offset + int64(utf8.RuneCountInString(text))
	page := TextPage{Offset: offset, Total: total, Text: text}
	if end < total {
		page.Next = &end
	}
	return page
}

func zoomRange(nodes []memory.Node, id, n int64) error {
	maxLevel := -1
	for _, node := range nodes {
		if node.Level > maxLevel {
			maxLevel = node.Level
		}
	}
	var roots []string
	for _, node := range nodes {
		if node.Level == maxLevel {
			roots = append(roots, fmt.Sprintf("%d..%d %s", node.FromSeq, node.ToSeq, node.State))
		}
	}
	if len(roots) == 0 {
		roots = []string{"none"}
	}
	return invalid("id", fmt.Sprintf("invalid zoom range %d..%d; roots: %s", id, id+n-1, strings.Join(roots, ", ")), nil)
}

func (s *Service) MemoryZoom(ctx context.Context, in MemoryZoomInput) (application.Read[MemoryZoom], error) {
	out := MemoryZoom{ID: in.ID, N: in.N}
	rev, err := s.snapshot(ctx, string(MemoryZoomName), memoryScopes, in, func(q db.Querier) error {
		marks, err := threadMarks(ctx, q, []int64{in.ThreadID})
		if err != nil {
			return err
		}
		out.Marks = marks | tool.Messages
		nodes, err := memory.Tree(ctx, q, in.ThreadID)
		if err != nil {
			return err
		}
		if in.N&(in.N-1) != 0 {
			return zoomRange(nodes, in.ID, in.N)
		}
		if in.N == 1 {
			return s.zoomLeaf(ctx, q, nodes, in, &out)
		}
		level := bits.TrailingZeros64(uint64(in.N))
		var parent *memory.Node
		for i := range nodes {
			if nodes[i].Level == level && nodes[i].FromSeq == in.ID && nodes[i].ToSeq == in.ID+in.N-1 {
				parent = &nodes[i]
				break
			}
		}
		if parent == nil {
			return zoomRange(nodes, in.ID, in.N)
		}
		for i := range nodes {
			if nodes[i].Level == level-1 && (nodes[i].Index == parent.Index*2 || nodes[i].Index == parent.Index*2+1) {
				out.Children = append(out.Children, ZoomChild{ID: nodes[i].FromSeq, N: in.N / 2, To: nodes[i].ToSeq, State: nodes[i].State, Text: nodes[i].Text})
			}
		}
		if len(out.Children) != 2 {
			return zoomRange(nodes, in.ID, in.N)
		}
		return nil
	})
	return application.Read[MemoryZoom]{Revision: rev, Data: out}, err
}

func (s *Service) zoomLeaf(ctx context.Context, q db.Querier, nodes []memory.Node, in MemoryZoomInput, out *MemoryZoom) error {
	var found *memory.Node
	for i := range nodes {
		if nodes[i].Level == 0 && nodes[i].FromSeq == in.ID {
			found = &nodes[i]
			break
		}
	}
	if found == nil {
		return zoomRange(nodes, in.ID, 1)
	}
	if len(found.ResultIDs) > 0 {
		out.ResultID = found.ResultIDs[0]
		return nil
	}
	messages, err := journal.MessagesOf(ctx, q, in.ThreadID)
	if err != nil {
		return err
	}
	var body string
	for _, m := range messages {
		if m.Seq != in.ID {
			continue
		}
		blocks, err := block.DecodeAll(m.Content)
		if err != nil {
			return err
		}
		var parts []string
		for _, b := range blocks {
			if b.Kind == block.Text {
				parts = append(parts, b.Text())
			}
		}
		body = strings.Join(parts, "\n")
		break
	}
	page := pageOf(body, in.At, int64(assistant.TuningAgentRecallPageChars))
	out.Page = &page
	return nil
}

func (s *Service) MemorySearch(ctx context.Context, in MemorySearchInput) (application.Read[MemorySearch], error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return application.Read[MemorySearch]{}, invalid("query", "query is required", nil)
	}
	out := MemorySearch{Hits: []MemoryHit{}, Marks: tool.Messages}
	rev, err := s.snapshot(ctx, string(MemorySearchName), memoryScopes, in, func(q db.Querier) error {
		found, err := memory.Search(ctx, q, in.ThreadID, memory.SearchFilter{Query: query, Project: in.Project, Limit: in.Limit})
		if err != nil {
			return err
		}
		page := assistant.TuningAgentRecallPageChars
		seen := map[[3]int64]bool{}
		for _, h := range found {
			key := [3]int64{h.ThreadID, h.FromSeq, h.ToSeq}
			if seen[key] {
				continue
			}
			seen[key] = true
			h.Text = clipSummary(h.Text, page)
			out.Hits = append(out.Hits, MemoryHit{Hit: h, Own: h.ThreadID == in.ThreadID})
			out.Marks |= h.Marks
		}
		return nil
	})
	return application.Read[MemorySearch]{Revision: rev, Data: out}, err
}

func clipSummary(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

func (s *Service) ResultRecall(ctx context.Context, in RecallInput) (application.Read[RecallPage], error) {
	out := RecallPage{ResultID: in.ResultID}
	rev, err := s.snapshot(ctx, string(ResultRecallName), memoryScopes, in, func(q db.Querier) error {
		var body string
		err := q.QueryRowContext(ctx,
			"SELECT r.body FROM tool_results r JOIN runs ON runs.id = r.run_id WHERE r.result_id = ? AND runs.thread_id = ?",
			in.ResultID, in.ThreadID).Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound(fmt.Sprintf("no stored result %d in this thread", in.ResultID))
		}
		if err != nil {
			return err
		}
		out.TextPage = pageOf(body, in.Offset, in.Limit)
		return nil
	})
	return application.Read[RecallPage]{Revision: rev, Data: out}, err
}
