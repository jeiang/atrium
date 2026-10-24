package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant"
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
	MaxTokens       = 16384
	MaxTokensDeep   = 65536
	Research        = "research"
	webSearchResult = "web_search_tool_result"

	Verify              = "atrium was interrupted while this call was in flight; it may or may not have taken effect. Check the current state before retrying it."
	CompletedUncaptured = "This call completed before an interruption but its result was not captured. Check the current state."
)

var log = logx.Logger("atrium.agent")

type RunLimit string

func (e RunLimit) Error() string { return string(e) }

type LeaseLost string

func (e LeaseLost) Error() string { return string(e) }

type Runner struct {
	Registry      *registry.Registry
	Profile       func(ctx context.Context, q db.Querier) (string, error)
	SkillsIndex   func(ctx context.Context, q db.Querier) (string, error)
	ExpandSkills  func(ctx context.Context, q db.Querier, text string) (expanded string, used []string, err error)
	ResearchBatch Batch
	CommitSavings int64
}

func (r *Runner) Expand(ctx context.Context, q db.Querier, kind tool.Kind, text string) (string, []string, error) {
	if r.ExpandSkills == nil || kind == tool.Sub {
		return text, nil, nil
	}
	return r.ExpandSkills(ctx, q, text)
}

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

	callsKey string
	repeats  int

	doomThreshold int
	stepsKey      string
	toolChoice    *anthropic.ToolChoice
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func (r *Runner) Run(ctx context.Context, d *db.DB, cfg config.Config, env config.Env, client *anthropic.Client, runID int64, o Options) (*journal.Run, error) {
	s, err := r.prepare(ctx, d, cfg, env, client, runID, o)
	if err != nil {
		return nil, err
	}
	final := journal.Failed
	var errText *string
	stopBeat := s.heartbeat(ctx)
	err = s.body(ctx)
	stopBeat()
	switch {
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
		if ttl, err = cacheTTL(ctx, d.Read); err != nil {
			return nil, err
		}
	}
	steps, err := maxSteps(ctx, d.Read, kind)
	if err != nil {
		return nil, err
	}
	doomThreshold, err := doomLoopThreshold(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	maxCost, err := maxRunCost(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	tuning, err := actx.ReadTuning(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	webSearchMaxUses, err := readWebSearchMaxUses(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	model, err := pricing.Lookup(found.Model)
	if err != nil {
		return nil, err
	}
	limits := actx.LimitsFor(min(threads.ContextLimitOf(*thread), model.ContextWindow), tuning)
	req, err := r.request(ctx, d.Read, *thread, kind, model.ID, found.Effort, tools, prompts.PrefixCacheControl(kind, ttl), deref(o.Limits.SystemExtra), allow, limits, webSearchMaxUses)
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
		writer: writer, env: env, steps: steps, stepsKey: maxStepsKeys[kind], doomThreshold: doomThreshold, maxCost: maxCost, savings: int64(tuning.CommitSavings),
		x: &dispatcher{d: d, tc: tc, byName: byName, now: now, batch: r.ResearchBatch, tuning: tuning},
	}
	s.x.guard = s.renew
	s.x.lost, s.x.abort = context.WithCancel(context.Background())
	if o.Limits.MaxSteps != nil {
		s.steps = int(*o.Limits.MaxSteps)
		s.stepsKey = assistant.KeySubagentMaxSteps
	}
	if s.toolChoice, err = forcedToolChoice(o.Limits.ToolChoice, model, byName); err != nil {
		return nil, err
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
	ok, err := journal.RenewLease(ctx, s.d.Write, s.run.ID, deref(s.run.Owner), s.now())
	if err != nil {
		return err
	}
	if !ok {
		return LeaseLost(fmt.Sprintf("run %d was taken over by another process", s.run.ID))
	}
	return nil
}

func (s *state) heartbeat(ctx context.Context) func() {
	lease, err := journal.LeaseSeconds(ctx, s.d.Read)
	if err != nil || lease <= 0 {
		return func() {}
	}
	beatCtx, cancel := context.WithCancel(ctx)
	every := time.Duration(lease / 3 * float64(time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-beatCtx.Done():
				return
			case <-t.C:
				if err := s.renew(beatCtx); errors.As(err, new(LeaseLost)) {
					s.x.abort()
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
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

func (s *state) freshTurn(ctx context.Context) (bool, error) {
	last, err := journal.LastMessage(ctx, s.d.Read, s.thread.ID)
	if err != nil || last == nil {
		return false, err
	}
	return last.Role == journal.User && journal.HasText(last.Content), nil
}

func PendingUses(ctx context.Context, q db.Querier, threadID int64) (*journal.Message, []ToolUse, error) {
	last, err := journal.LastMessage(ctx, q, threadID)
	if err != nil || last == nil || last.Role != journal.Assistant {
		return nil, nil, err
	}
	uses, err := toolUses(last.Content)
	return last, uses, err
}

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
	params.ToolChoice, s.toolChoice = s.toolChoice, nil
	ledgerCtx := context.WithoutCancel(ctx)
	response, err := s.client.Stream(ctx, params, func(ev anthropic.Event) error { return s.writer.Observe(ledgerCtx, ev) })
	if err != nil {
		return nil, err
	}
	return response, s.writer.Flush(context.WithoutCancel(ctx))
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
	return RunLimit(fmt.Sprintf("stopped: step cap reached after %d steps in this %s run (%s)", s.steps, s.kind, s.stepsKey))
}

func (s *state) unattended(ctx context.Context) (bool, error) {
	root, err := threads.RootKind(ctx, s.d.Read, s.thread.ID)
	return root == threads.Cron || root == threads.Nightly, err
}

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
		s.callsKey, s.repeats = "", 0
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
		if _, err := actx.ClientCompact(ctx, s.d.Write, s.client, s.run.Model, active, s.thread.ID, s.run.ID, string(s.kind), s.limits.Hard, s.now(), s.limits.Tuning); err != nil {
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
	if err := journal.BumpSteps(context.WithoutCancel(ctx), s.d.Write, s.run.ID); err != nil {
		return false, nil, err
	}
	blocks, err := journalBlocks(response.Content)
	if err != nil {
		return false, nil, err
	}
	compacted := response.StopReason == actx.Compaction
	var stop error
	if !compacted {
		blocks, stop = judge(response, blocks)
	}
	clearEdits, cleared := actx.ClearsOf(response)
	entry := usage.Entry{
		TS: s.now(), ThreadID: s.thread.ID, RunID: s.run.ID, Kind: string(s.kind), Model: s.run.Model, Effort: s.run.Effort,
		ClearEdits: clearEdits, ClearedTokens: cleared, Compaction: int64(store.Bit(compacted)),
	}
	if _, err := usage.Record(context.WithoutCancel(ctx), s.d.Write, entry, response.Usage); err != nil {
		return false, nil, err
	}
	if err := s.renew(ctx); err != nil {
		return false, nil, err
	}
	seq, err := journal.AppendMessage(ctx, s.d, s.thread.ID, &s.run.ID, journal.Assistant, blocks, s.now(), new(s.req.sig))
	if err != nil {
		return false, nil, err
	}
	if stop != nil {
		return false, nil, stop
	}
	if compacted {
		_, err := actx.ServerCompacted(ctx, s.d.Write, s.thread.ID, s.run.ID, blocks, active, s.now(), s.limits.Tuning)
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
	if err := s.repeat(uses, results); err != nil {
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
