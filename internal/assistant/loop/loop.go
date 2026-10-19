// Package loop is agent/loop.py: one assistant run, step by step. Each step rechecks the budget and the
// run's lease, takes the operator's queued messages, compacts the context when the next request would not fit,
// streams one model call into the progress journal, meters it, and runs the tools the model asked for. Only
// the tools the registry offers the run's kind (and allowlist) are ever dispatched.
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/budget"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/py"
)

const (
	Model           = pricing.Model
	MaxSteps        = 24
	MaxTokens       = 16384
	MaxTokensDeep   = 65536
	MaxRunCostUSD   = 2.0
	CacheTTLKey     = "cache_ttl_chat"
	Research        = "research"
	webSearchResult = "web_search_tool_result"

	Verify              = "atrium was interrupted while this call was in flight; it may or may not have taken effect. Check the current state before retrying it."
	CompletedUncaptured = "This call completed before an interruption but its result was not captured. Check the current state."
)

// Owner is journal.OWNER: this process's identity in run leases.
var Owner = journal.NewOwner()

var log = logx.Logger("atrium.agent")

// RunLimit is RunLimit: the run hit its step, cost or context ceiling.
type RunLimit string

func (e RunLimit) Error() string { return string(e) }

// LeaseLost is LeaseLost: another process took the run over.
type LeaseLost string

func (e LeaseLost) Error() string { return string(e) }

// Runner holds what a run needs beyond its journal: the tool registry and the parts other packages own (the
// profile block, the skills index and /skill expansion, the parallel research batch). A nil part is absent.
// CommitSavings is context.COMMIT_SAVINGS, the staged savings that commit mid-loop; 0 is the default.
type Runner struct {
	Registry      *registry.Registry
	Profile       func(ctx context.Context, q db.Querier) (string, error)
	SkillsIndex   func(ctx context.Context, q db.Querier) (string, error)
	ExpandSkills  func(ctx context.Context, q db.Querier, text string) (expanded string, used []string, err error)
	ResearchBatch Batch
	CommitSavings int64
}

// Expand is skills.expand for operator text of the kinds that expand skills (chat, cron and nightly).
func (r *Runner) Expand(ctx context.Context, q db.Querier, kind tool.Kind, text string) (string, []string, error) {
	if r.ExpandSkills == nil || kind == tool.Sub {
		return text, nil, nil
	}
	return r.ExpandSkills(ctx, q, text)
}

// Options are loop.run's keyword arguments. Limits fields left nil take MaxSteps, MaxRunCostUSD, no role
// addendum and no allowlist.
type Options struct {
	Now    func() float64
	Embed  func(ctx context.Context, texts []string) ([][]float64, error)
	Limits journal.Limits
}

type state struct {
	r       *Runner
	d       *db.DB
	client  *anthropic.Client
	now     func() float64
	run     journal.Run
	thread  threads.Thread
	kind    tool.Kind
	limits  actx.Limits
	req     request
	writer  *progress.Writer
	x       *dispatcher
	env     config.Env
	steps   int
	maxCost float64
	savings int64
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// Run is run: it executes the run to its end and returns its row. A run that fails (budget, ceilings, a stop
// request, a failed model call) is finished as failed with the reason and is not an error; a lost lease
// leaves the run to its new owner, and a cancelled ctx leaves it running for the restart to resume.
func (r *Runner) Run(ctx context.Context, d *db.DB, cfg config.Config, env config.Env, client *anthropic.Client, runID int64, o Options) (*journal.Run, error) {
	s, err := r.prepare(ctx, d, cfg, env, client, runID, o)
	if err != nil {
		return nil, err
	}
	final := journal.Failed
	var errText *string
	switch err := s.body(ctx); {
	case err == nil:
		final = journal.Done
	case errors.As(err, new(LeaseLost)):
		log.Warn(err.Error()+"; leaving it to the new owner", logx.RunID, runID)
		return journal.GetRun(ctx, d.Read, runID)
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.As(err, new(budget.BudgetExceeded)), errors.As(err, new(RunLimit)), errors.Is(err, progress.ErrStopped):
		errText = new(err.Error())
	default:
		log.Error(fmt.Sprintf("run %d failed", runID), logx.RunID, runID, logx.Err, err)
		errText = new(py.ExceptionText(err))
	}
	if errText != nil {
		if err := journal.FinishRun(ctx, d.Write, runID, journal.Failed, s.now(), errText); err != nil {
			return nil, err
		}
	}
	if err := s.writer.Event(ctx, progress.RunDone, py.Dict{{Key: "state", Value: final}, {Key: "error", Value: optional(errText)}}); err != nil {
		return nil, err
	}
	return journal.GetRun(ctx, d.Read, runID)
}

func optional(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func (r *Runner) prepare(ctx context.Context, d *db.DB, cfg config.Config, env config.Env, client *anthropic.Client, runID int64, o Options) (*state, error) {
	found, err := journal.GetRun(ctx, d.Read, runID)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("loop: no run %d", runID)
	}
	thread, err := threads.Get(ctx, d.Read, found.ThreadID)
	if err != nil {
		return nil, err
	}
	if thread == nil {
		return nil, fmt.Errorf("loop: run %d: no thread %d", runID, found.ThreadID)
	}
	kind := tool.Kind(found.Kind)
	allow := o.Limits.AllowedTools
	tools, err := r.Registry.Run(ctx, kind, allow, d.Read)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]tool.Tool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
	}
	ttl := prompts.TTL5m
	if kind == tool.Chat {
		if stored, ok, err := budget.GetSetting(ctx, d.Read, CacheTTLKey); err != nil {
			return nil, err
		} else if ok {
			ttl = stored
		}
	}
	limits := actx.LimitsFor(threads.ContextLimitOf(*thread))
	req, err := r.request(ctx, d.Read, *thread, kind, found.Effort, tools, prompts.PrefixCacheControl(kind, ttl), deref(o.Limits.SystemExtra), allow, limits)
	if err != nil {
		return nil, err
	}
	urls, err := threads.AllowedURLs(ctx, d.Read, thread.ID)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(urls))
	for u := range urls {
		allowed[u] = struct{}{}
	}
	now := o.Now
	if now == nil {
		now = func() float64 { return float64(time.Now().UnixMicro()) / 1e6 }
	}
	writer := progress.NewWriter(d.Write, runID, now)
	tc := &tool.Context{
		DB: d, Cfg: cfg, Env: env, Kind: kind, ThreadID: thread.ID, RunID: runID, AllowedURLs: allowed,
		Embed: o.Embed, Now: now, Client: client, Progress: writer,
	}
	s := &state{
		r: r, d: d, client: client, now: now, run: *found, thread: *thread, kind: kind, limits: limits, req: req,
		writer: writer, env: env, steps: MaxSteps, maxCost: MaxRunCostUSD, savings: actx.CommitSavings,
		x: &dispatcher{d: d, tc: tc, byName: byName, now: now, batch: r.ResearchBatch},
	}
	if o.Limits.MaxSteps != nil {
		s.steps = int(*o.Limits.MaxSteps)
	}
	if o.Limits.MaxCostUSD != nil {
		s.maxCost = *o.Limits.MaxCostUSD
	}
	if r.CommitSavings != 0 {
		s.savings = r.CommitSavings
	}
	return s, nil
}

func (s *state) renew(ctx context.Context) error {
	ok, err := journal.RenewLease(ctx, s.d.Write, s.run.ID, Owner, s.now())
	if err != nil {
		return err
	}
	if !ok {
		return LeaseLost(fmt.Sprintf("run %d was taken over by another process", s.run.ID))
	}
	return nil
}

func (s *state) runStart(ctx context.Context) error {
	return s.writer.Event(ctx, progress.RunStart, py.Dict{
		{Key: "thread_id", Value: s.thread.ID},
		{Key: "thread_kind", Value: s.thread.Kind},
		{Key: "role", Value: optional(s.thread.Role)},
		{Key: "title", Value: optional(s.thread.Title)},
		{Key: "effort", Value: s.run.Effort},
	})
}

// freshTurn is fresh_turn: the run starts on a new operator message, a natural commit boundary.
func (s *state) freshTurn(ctx context.Context) (bool, error) {
	last, err := journal.LastMessage(ctx, s.d.Read, s.thread.ID)
	if err != nil || last == nil {
		return false, err
	}
	return last.Role == journal.User && journal.HasText(last.Content), nil
}

// PendingUses is pending_uses: the tool_use blocks of the thread's last message when it is an assistant turn
// still waiting for its results.
func PendingUses(ctx context.Context, q db.Querier, threadID int64) (*journal.Message, []ToolUse, error) {
	last, err := journal.LastMessage(ctx, q, threadID)
	if err != nil || last == nil || last.Role != journal.Assistant {
		return nil, nil, err
	}
	uses, err := toolUses(last.Content)
	return last, uses, err
}

// searchURLs is search_urls: the result URLs of web_search_tool_result blocks.
func searchURLs(blocks []json.RawMessage) []string {
	var urls []string
	for _, raw := range blocks {
		if blockType(raw) != webSearchResult {
			continue
		}
		var b struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		var items []json.RawMessage
		if json.Unmarshal(b.Content, &items) != nil {
			continue
		}
		for _, item := range items {
			var r struct {
				URL any `json:"url"`
			}
			if json.Unmarshal(item, &r) != nil {
				continue
			}
			if u, ok := r.URL.(string); ok && u != "" {
				urls = append(urls, u)
			}
		}
	}
	return urls
}

func (s *state) allowURLs(ctx context.Context, urls []string) error {
	if len(urls) == 0 {
		return nil
	}
	for _, u := range urls {
		s.x.tc.AllowedURLs[u] = struct{}{}
	}
	return threads.AddAllowedURLs(ctx, s.d, s.thread.ID, urls)
}

// injectQueued is inject_queued: messages the operator sent while the run was busy join the conversation as
// user turns before the next model call. They are operator text: their URLs join the allowlist and their
// /skill references expand.
func (s *state) injectQueued(ctx context.Context) (bool, error) {
	rows, err := journal.TakeQueued(ctx, s.d.Write, s.thread.ID, s.run.ID, s.now())
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		text := row.Text
		if err := s.allowURLs(ctx, threads.URLsIn(text)); err != nil {
			return false, err
		}
		if text, _, err = s.r.Expand(ctx, s.d.Write, s.kind, text); err != nil {
			return false, err
		}
		content := append(append([]json.RawMessage{}, row.Files...), actx.TextBlock(text))
		if _, err := journal.AppendMessage(ctx, s.d, s.thread.ID, &s.run.ID, journal.User, content, s.now(), nil); err != nil {
			return false, err
		}
		if err := s.writer.Event(ctx, progress.User, py.Dict{{Key: "text", Value: text}, {Key: "queue_id", Value: row.ID}}); err != nil {
			return false, err
		}
	}
	return len(rows) > 0, nil
}

func (s *state) active(ctx context.Context, loc *time.Location) ([]actx.Message, error) {
	active, _, err := actx.ActiveMessages(ctx, s.d.Read, s.thread.ID, s.req.sig)
	if err != nil {
		return nil, err
	}
	runs, err := journal.RunsOf(ctx, s.d.Read, s.thread.ID)
	if err != nil {
		return nil, err
	}
	return stampMessages(active, runs, loc), nil
}

func (s *state) size(ctx context.Context, active []actx.Message, a *anchor) (int64, error) {
	sent, err := withProjectFiles(ctx, s.d.Read, s.thread, active)
	if err != nil {
		return 0, err
	}
	return s.req.estimate(sent, a)
}

func (s *state) stream(ctx context.Context, messages []json.RawMessage) (*anthropic.Message, error) {
	params := s.req.params
	params.Messages = messages
	response, err := s.client.Stream(ctx, params, func(ev anthropic.Event) error { return s.writer.Observe(ctx, ev) })
	if err != nil {
		return nil, err
	}
	return response, s.writer.Flush(ctx)
}

func (s *state) settlePending(ctx context.Context) error {
	last, pending, err := PendingUses(ctx, s.d.Read, s.thread.ID)
	if err != nil || len(pending) == 0 {
		return err
	}
	results := make([]json.RawMessage, len(pending))
	for i, use := range pending {
		call, err := journal.GetToolCall(ctx, s.d.Read, deref(last.RunID), use.ID)
		if err != nil {
			return err
		}
		if results[i], err = s.x.settle(ctx, use, call); err != nil {
			return err
		}
	}
	_, err = journal.AppendMessage(ctx, s.d, s.thread.ID, &s.run.ID, journal.User, results, s.now(), nil)
	return err
}

// body is the try block of run: nil once the run finished done.
func (s *state) body(ctx context.Context) error {
	if err := s.runStart(ctx); err != nil {
		return err
	}
	boundary, err := s.freshTurn(ctx)
	if err != nil {
		return err
	}
	if err := s.settlePending(ctx); err != nil {
		return err
	}
	if _, err := actx.Commit(ctx, s.d.Write, s.thread.ID, s.run.ID, s.now(), boundary); err != nil {
		return err
	}
	loc, err := config.Location(s.env)
	if err != nil {
		return err
	}
	unattended, err := s.unattended(ctx)
	if err != nil {
		return err
	}
	var last *anchor
	for range s.steps {
		done, next, err := s.step(ctx, loc, unattended, last)
		if err != nil || done {
			return err
		}
		last = next
	}
	return RunLimit(fmt.Sprintf("stopped after %d steps", s.steps))
}

func (s *state) unattended(ctx context.Context) (bool, error) {
	root, err := threads.RootKind(ctx, s.d.Read, s.thread.ID)
	return root == threads.Cron || root == threads.Nightly, err
}

// step is one iteration of the loop: done once the run finished, else the anchor for the next step.
func (s *state) step(ctx context.Context, loc *time.Location, unattended bool, last *anchor) (bool, *anchor, error) {
	if _, err := budget.Check(ctx, s.d.Read, s.now(), loc, unattended); err != nil {
		return false, nil, err
	}
	if err := s.renew(ctx); err != nil {
		return false, nil, err
	}
	if err := s.writer.CheckStop(ctx, true); err != nil {
		return false, nil, err
	}
	injected, err := s.injectQueued(ctx)
	if err != nil {
		return false, nil, err
	}
	if injected {
		last = nil
	}
	spent, err := usage.RunCost(ctx, s.d.Read, s.run.ID)
	if err != nil {
		return false, nil, err
	}
	if spent >= s.maxCost {
		return false, nil, RunLimit(fmt.Sprintf("run cost ceiling reached ($%s of $%s)", py.FormatFixed(spent, 2), py.FormatFixed(s.maxCost, 2)))
	}
	active, err := s.active(ctx, loc)
	if err != nil {
		return false, nil, err
	}
	size, err := s.size(ctx, active, last)
	if err != nil {
		return false, nil, err
	}
	if size > s.limits.Preflight {
		log.Info(fmt.Sprintf("run %d: next request ~%d tokens, compacting on the client", s.run.ID, size), logx.RunID, s.run.ID)
		if _, err := actx.ClientCompact(ctx, s.d.Write, s.client, Model, active, s.thread.ID, s.run.ID, string(s.kind), s.limits.Hard, s.now()); err != nil {
			return false, nil, err
		}
		last = nil
		if active, err = s.active(ctx, loc); err != nil {
			return false, nil, err
		}
		if size, err = s.size(ctx, active, last); err != nil {
			return false, nil, err
		}
	}
	if size > s.limits.Hard {
		return false, nil, RunLimit(fmt.Sprintf("next request would be about %d tokens, over the %d limit", size, s.limits.Hard))
	}
	sent, err := withProjectFiles(ctx, s.d.Read, s.thread, active)
	if err != nil {
		return false, nil, err
	}
	messages, err := s.req.buildMessages(ctx, s.d.Read, files.Scope{ThreadID: s.thread.ID, ProjectID: s.thread.ProjectID}, sent)
	if err != nil {
		return false, nil, err
	}
	response, err := s.stream(ctx, messages)
	if err != nil {
		return false, nil, err
	}
	if err := journal.BumpSteps(ctx, s.d.Write, s.run.ID); err != nil {
		return false, nil, err
	}
	blocks, err := journalBlocks(response.Content)
	if err != nil {
		return false, nil, err
	}
	compacted := response.StopReason == actx.Compaction
	clearEdits, cleared := actx.ClearsOf(response)
	entry := usage.Entry{
		TS: s.now(), ThreadID: s.thread.ID, RunID: s.run.ID, Kind: string(s.kind), Model: Model, Effort: s.run.Effort,
		ClearEdits: clearEdits, ClearedTokens: cleared, Compaction: int64(store.Bit(compacted)),
	}
	if _, err := usage.Record(ctx, s.d.Write, entry, response.Usage); err != nil {
		return false, nil, err
	}
	seq, err := journal.AppendMessage(ctx, s.d, s.thread.ID, &s.run.ID, journal.Assistant, blocks, s.now(), new(s.req.sig))
	if err != nil {
		return false, nil, err
	}
	if compacted {
		_, err := actx.ServerCompacted(ctx, s.d.Write, s.thread.ID, s.run.ID, blocks, active, s.now())
		return false, nil, err
	}
	next := &anchor{tokens: actx.HistoryTokens(response.Usage), seq: seq}
	if err := s.allowURLs(ctx, searchURLs(blocks)); err != nil {
		return false, nil, err
	}
	uses, err := toolUses(blocks)
	if err != nil {
		return false, nil, err
	}
	if len(uses) > 0 {
		err := s.answer(ctx, uses, &next)
		return false, next, err
	}
	if response.StopReason == anthropic.StopPauseTurn {
		return false, next, nil
	}
	done, err := journal.FinishUnlessQueued(ctx, s.d, s.run.ID, s.thread.ID, journal.Done, s.now())
	return done, next, err
}

// answer runs the response's tool calls, journals their results and commits staged context edits once they
// save enough (which invalidates the anchor).
func (s *state) answer(ctx context.Context, uses []ToolUse, next **anchor) error {
	results, err := s.x.executeAll(ctx, uses)
	if err != nil {
		return err
	}
	if err := s.renew(ctx); err != nil {
		return err
	}
	if _, err := journal.AppendMessage(ctx, s.d, s.thread.ID, &s.run.ID, journal.User, results, s.now(), nil); err != nil {
		return err
	}
	staged, err := actx.StagedSavings(ctx, s.d.Read, s.thread.ID)
	if err != nil || staged < s.savings {
		return err
	}
	committed, err := actx.Commit(ctx, s.d.Write, s.thread.ID, s.run.ID, s.now(), false)
	if committed != nil {
		*next = nil
	}
	return err
}
