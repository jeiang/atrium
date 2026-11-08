package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/budget"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/obs"
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
	Registry *registry.Registry
	Undoers  *writes.Registry
	Profile  func(ctx context.Context, q db.Querier) (string, error)
}

type Options struct {
	Now    func() float64
	Embed  func(ctx context.Context, texts []string) ([][]float64, error)
	Limits journal.Limits
}

type state struct {
	r         *Runner
	d         *db.DB
	client    *anthropic.Client
	now       func() float64
	run       journal.Run
	thread    threads.Thread
	kind      tool.Kind
	limits    actx.Limits
	req       request
	writer    *progress.Writer
	x         *dispatcher
	env       config.Env
	steps     int
	stepIndex int
	maxCost   float64
	ttl       string

	callsKey string
	repeats  int

	doomThreshold int
	stepsKey      string
	invalid       error
	toolChoice    *anthropic.ToolChoice
	admission     admission
	hist          *history
}

type admission struct {
	allow            registry.Allow
	model            string
	cc               prompts.CacheControl
	extra            string
	webSearchMaxUses int
	dynamicFiltering bool
}

func (s *state) admit(ctx context.Context) (registry.Set, error) {
	a := s.admission
	set, err := s.r.Registry.Admit(ctx, s.thread.ID, s.kind, a.allow, s.d.Read)
	if err != nil {
		return registry.Set{}, err
	}
	req, err := s.r.request(ctx, s.d.Read, s.thread, s.kind, a.model, s.run.Effort, set, a.cc, a.extra, a.webSearchMaxUses, a.dynamicFiltering)
	if err != nil {
		return registry.Set{}, err
	}
	byName := make(map[string]tool.Tool, len(set.Dispatch))
	for _, t := range set.Dispatch {
		byName[t.Name] = t
	}
	s.req, s.x.byName = req, byName
	return set, nil
}

func (s *state) readmit(ctx context.Context) error {
	if !s.x.tainted.Swap(false) {
		return nil
	}
	_, err := s.admit(ctx)
	return err
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func (r *Runner) Run(ctx context.Context, d *db.DB, cfg config.Config, env config.Env, client *anthropic.Client, runID int64, o Options) (*journal.Run, error) {
	started := time.Now()
	ctx, span := obs.Start(ctx, "agent.run", trace.WithAttributes(obs.Int64(obs.RunID, runID)))
	defer span.End()
	s, err := r.prepare(ctx, d, cfg, env, client, runID, o)
	if err != nil {
		obs.Fail(span, "prepare")
		now := assistant.Now()
		if o.Now != nil {
			now = o.Now()
		}
		_ = journal.FinishRun(ctx, d.Write, runID, journal.Failed, now, new(err.Error()))
		return nil, err
	}
	span.SetAttributes(
		obs.Int64(obs.ThreadID, s.thread.ID),
		obs.String(obs.RunKind, string(s.kind)),
		obs.String(obs.GenAIRequestModel, s.run.Model),
		obs.String(obs.RunEffort, s.run.Effort),
	)
	final := journal.Failed
	var errText *string
	stopBeat := s.heartbeat(ctx)
	err = s.body(ctx)
	stopBeat()
	var api *anthropic.APIError
	if errors.As(err, &api) && api.SpendCapped() {
		err = budget.SpendCap()
	}
	switch {
	case err == nil:
		final = journal.Done
	case errors.As(err, new(LeaseLost)):
		log.Warn(err.Error()+"; leaving it to the new owner", logx.RunID, runID)
		finished, ferr := journal.GetRun(ctx, d.Read, runID)
		s.observeRun(started, span, finished)
		return finished, ferr
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.As(err, new(*tool.Park)):
		final = journal.Parked
	case errors.As(err, new(budget.BudgetExceeded)), errors.As(err, new(RunLimit)), errors.Is(err, progress.ErrStopped):
		errText = new(err.Error())
	default:
		log.Error(fmt.Sprintf("run %d failed", runID), logx.RunID, runID, logx.Err, err)
		errText = new(err.Error())
	}
	if errText != nil {
		if rerr := memory.Rebuild(ctx, d.Write, s.thread.ID, s.now()); rerr != nil {
			log.Error(fmt.Sprintf("thread %d memory tree rebuild failed", s.thread.ID), logx.RunID, runID, logx.Err, rerr)
		}
		if err := journal.FinishRun(ctx, d.Write, runID, journal.Failed, s.now(), errText); err != nil {
			return nil, err
		}
	}
	if final == journal.Parked {
		if err := journal.ParkRun(ctx, d.Write, runID, s.now()); err != nil {
			return nil, err
		}
	}
	if err := s.writer.Event(ctx, progress.RunDone, map[string]any{"state": final, "error": optional(errText)}); err != nil {
		return nil, err
	}
	finished, err := journal.GetRun(ctx, d.Read, runID)
	s.observeRun(started, span, finished)
	return finished, err
}

func (s *state) observeRun(started time.Time, span trace.Span, run *journal.Run) {
	if run == nil {
		return
	}
	kind := string(s.kind)
	span.SetAttributes(obs.String(obs.RunState, run.State), obs.Int64(obs.RunStepCount, run.Steps))
	obs.Runs.Add(1, kind, run.State)
	obs.RunSteps.Observe(float64(run.Steps), kind)
	obs.RunDuration.Observe(time.Since(started).Seconds(), kind)
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
	kind, ok := tool.ParseKind(found.Kind)
	if !ok {
		return nil, fmt.Errorf("loop: run %d has unknown kind %q", runID, found.Kind)
	}
	allow := registry.All()
	if o.Limits.AllowedTools != nil {
		allow = registry.Only(o.Limits.AllowedTools)
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
	tuning, err := actx.ReadTuning(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	webSearchMaxUses, err := readWebSearchMaxUses(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	dynamicFiltering, err := readWebSearchDynamicFiltering(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	model, err := pricing.Lookup(found.Model)
	if err != nil {
		return nil, err
	}
	maxCost, err := budget.RunCap(ctx, d.Read, model.ID)
	if err != nil {
		return nil, err
	}
	limits := actx.LimitsFor(min(threads.ContextLimitOf(*thread), model.ContextWindow), tuning)
	extra := deref(o.Limits.SystemExtra) + memoryPrompt
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
		now = assistant.Now
	}
	writer := progress.NewWriter(d.Write, runID, now)
	tc := &tool.Context{
		DB: d, Cfg: cfg, Env: env, Kind: kind, ThreadID: thread.ID, RunID: runID, AllowedURLs: allowed,
		Embed: o.Embed, Now: now, Client: client, Progress: writer, Undoers: r.Undoers,
	}
	s := &state{
		r: r, d: d, client: client, now: now, run: *found, thread: *thread, kind: kind, limits: limits,
		writer: writer, env: env, steps: steps, stepsKey: maxStepsKey(kind), doomThreshold: doomThreshold, maxCost: maxCost, ttl: ttl,
		x: &dispatcher{d: d, tc: tc, now: now, tuning: tuning},
		admission: admission{
			allow: allow, model: model.ID, cc: prompts.PrefixCacheControl(kind, ttl), extra: extra,
			webSearchMaxUses: webSearchMaxUses, dynamicFiltering: dynamicFiltering,
		},
	}
	set, err := s.admit(ctx)
	if err != nil {
		return nil, err
	}
	s.x.guard = s.renew
	s.x.lost, s.x.abort = context.WithCancel(context.Background())
	if o.Limits.MaxSteps != nil {
		s.steps = int(*o.Limits.MaxSteps)
	}
	s.toolChoice, s.invalid = forcedToolChoice(o.Limits.ToolChoice, model, set)
	if o.Limits.MaxCostUSD != nil {
		s.maxCost = *o.Limits.MaxCostUSD
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
				if err := s.renew(beatCtx); err != nil {
					log.Warn("could not renew the run lease; aborting its tools", logx.RunID, s.run.ID, logx.Err, err)
					s.x.abort()
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (s *state) runStart(ctx context.Context) error {
	return s.writer.Event(ctx, progress.RunStart, map[string]any{"thread_id": s.thread.ID, "thread_kind": string(s.thread.Kind), "role": optional(s.thread.Role), "title": optional(s.thread.Title), "effort": s.run.Effort})
}

func PendingUses(ctx context.Context, q db.Querier, threadID int64) (*journal.Message, []ToolUse, error) {
	last, err := journal.LastMessage(ctx, q, threadID)
	if err != nil || last == nil || last.Role != journal.Assistant {
		return nil, nil, err
	}
	uses, err := toolUses(last.Content)
	return last, uses, err
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
	queued, err := journal.Queued(ctx, s.d.Read, s.thread.ID)
	if err != nil {
		return false, err
	}
	for _, row := range queued {
		if err := s.allowURLs(ctx, threads.URLsIn(row.Text)); err != nil {
			return false, err
		}
	}
	texts := make(map[int64]string, len(queued))
	taken, err := journal.ConsumeQueued(ctx, s.d, s.thread.ID, s.run.ID, s.now(), func(q db.Querier, row journal.QueuedMessage) error {
		texts[row.ID] = row.Text
		content := append(append([]json.RawMessage{}, row.Files...), actx.TextBlock(row.Text))
		_, err := journal.AppendMessageTx(ctx, q, s.thread.ID, &s.run.ID, journal.User, content, s.now(), nil)
		return err
	})
	if err != nil {
		return false, err
	}
	for _, row := range taken {
		if err := s.writer.Event(ctx, progress.User, map[string]any{"text": texts[row.ID], "queue_id": row.ID}); err != nil {
			return false, err
		}
	}
	return len(taken) > 0, nil
}

const memoryPrompt = "\n\nMemory view: the first user message opens with this thread's older history as a memory tree; " +
	"the messages after it are verbatim. Each memory line carries a message range and a zoom id; call zoom(id, n) to open " +
	"a summary's two children, or zoom(id, 1) to read one message's source, when the summary is not enough."

func (s *state) stream(ctx context.Context, messages []json.RawMessage) (*anthropic.Message, int64, error) {
	params := s.req.params
	params.Messages = messages
	params.ToolChoice, s.toolChoice = s.toolChoice, nil
	ledgerCtx := context.WithoutCancel(ctx)
	started := time.Now()
	response, err := s.client.Stream(ctx, params, func(ev anthropic.Event) error { return s.writer.Observe(ledgerCtx, ev) })
	wall := time.Since(started).Milliseconds()
	if err != nil {
		return response, wall, err
	}
	return response, wall, s.writer.Flush(context.WithoutCancel(ctx))
}

func (s *state) recordUsage(ctx context.Context, response *anthropic.Message, toolCalls, wall int64, partial bool) (float64, error) {
	clearEdits, cleared := actx.ClearsOf(response)
	entry := usage.Entry{
		TS: s.now(), RequestID: response.ID, ThreadID: s.thread.ID, RunID: s.run.ID, Kind: string(s.kind), Model: s.run.Model,
		Effort: s.run.Effort, ToolCalls: toolCalls, WallMS: wall, Partial: partial,
		ClearEdits: clearEdits, ClearedTokens: cleared, Compaction: int64(store.Bit(response.StopReason == block.Compaction)),
	}
	return usage.Record(context.WithoutCancel(ctx), s.d, entry, response.Usage)
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
	if s.invalid != nil {
		return s.invalid
	}
	if err := s.runStart(ctx); err != nil {
		return err
	}
	if err := s.settlePending(ctx); err != nil {
		return err
	}
	loc, err := config.From(ctx).Location(s.env)
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

func (s *state) step(ctx context.Context, loc *time.Location, unattended bool, last *anchor) (done bool, next *anchor, err error) {
	s.stepIndex++
	ctx, span := obs.Start(ctx, "agent.step", trace.WithAttributes(
		obs.Int(obs.StepIndex, s.stepIndex),
		obs.String(obs.RunKind, string(s.kind)),
	))
	defer func() {
		if err != nil {
			obs.Fail(span, spanError(err))
		}
		span.End()
	}()
	if err := s.readmit(ctx); err != nil {
		return false, nil, err
	}
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
	spent, err := usage.TreeCost(ctx, s.d.Read, s.run.ID)
	if err != nil {
		return false, nil, err
	}
	if spent >= s.maxCost {
		return false, nil, RunLimit(fmt.Sprintf("run cost ceiling reached ($%s of $%s)", strconv.FormatFloat(spent, 'f', 2, 64), strconv.FormatFloat(s.maxCost, 'f', 2, 64)))
	}
	h, err := s.history(ctx)
	if err != nil {
		return false, nil, err
	}
	active, err := s.active(ctx, loc, h)
	if err != nil {
		return false, nil, err
	}
	size, err := s.req.estimate(active, last)
	if err != nil {
		return false, nil, err
	}
	span.SetAttributes(obs.Float(obs.RunCostUSD, spent), obs.Int64(obs.ContextSize, size))
	if size > s.limits.Hard {
		return false, nil, RunLimit(fmt.Sprintf("next request would be about %d tokens, over the %d limit", size, s.limits.Hard))
	}
	if err := s.reserve(ctx, size); err != nil {
		return false, nil, err
	}
	messages, err := s.req.buildMessages(ctx, s.d.Read, files.ScopeOf(s.thread.ID, s.thread.ProjectID), active)
	if err != nil {
		return false, nil, err
	}
	reqCtx, reqSpan := obs.Start(ctx, "gen_ai.request", trace.WithAttributes(
		obs.String(obs.GenAIRequestModel, s.run.Model),
		obs.String(obs.RunKind, string(s.kind)),
	))
	response, wall, err := s.stream(reqCtx, messages)
	if err != nil {
		if response != nil && response.ID != "" {
			cost, rerr := s.recordUsage(ctx, response, 0, wall, true)
			genAIAttrs(reqSpan, response, wall, cost, true)
			if rerr != nil {
				obs.Fail(reqSpan, "usage")
				reqSpan.End()
				return false, nil, rerr
			}
		}
		obs.Fail(reqSpan, spanError(err))
		reqSpan.End()
		return false, nil, err
	}
	if err := journal.BumpSteps(context.WithoutCancel(ctx), s.d.Write, s.run.ID); err != nil {
		obs.Fail(reqSpan, "journal")
		reqSpan.End()
		return false, nil, err
	}
	blocks := response.Content
	var stop error
	blocks, stop = judge(response, blocks)
	uses, err := toolUses(blocks)
	if err != nil {
		obs.Fail(reqSpan, "decode")
		reqSpan.End()
		return false, nil, err
	}
	cost, err := s.recordUsage(ctx, response, int64(len(uses)), wall, false)
	genAIAttrs(reqSpan, response, wall, cost, false)
	if err != nil {
		obs.Fail(reqSpan, "usage")
	}
	reqSpan.End()
	if err != nil {
		return false, nil, err
	}
	if err := s.renew(ctx); err != nil {
		return false, nil, err
	}
	seq, err := journal.AppendMessage(ctx, s.d, s.thread.ID, &s.run.ID, journal.Assistant, blocks, s.now(), new(s.req.sig))
	if err != nil {
		return false, nil, err
	}
	searched, err := block.HasKind(blocks, block.WebSearchResult)
	if err != nil {
		return false, nil, err
	}
	if searched {
		if err := threads.Taint(context.WithoutCancel(ctx), s.d.Write, s.thread.ID, s.now()); err != nil {
			return false, nil, err
		}
		s.x.tainted.Store(true)
	}
	if stop != nil {
		return false, nil, stop
	}
	next = &anchor{tokens: actx.HistoryTokens(response.Usage), seq: seq}
	urls, err := block.SearchURLs(blocks)
	if err != nil {
		return false, nil, err
	}
	if err := s.allowURLs(ctx, urls); err != nil {
		return false, nil, err
	}
	if len(uses) > 0 {
		err := s.answer(ctx, uses)
		return false, next, err
	}
	if response.StopReason == anthropic.StopPauseTurn {
		return false, next, nil
	}
	if err := memory.Rebuild(ctx, s.d.Write, s.thread.ID, s.now()); err != nil {
		return false, next, err
	}
	done, err = journal.FinishUnlessQueued(ctx, s.d, s.run.ID, s.thread.ID, journal.Done, s.now())
	return done, next, err
}

func (s *state) reserve(ctx context.Context, size int64) error {
	cold, err := budget.ColdRequestUSD(s.run.Model, size, s.ttl)
	if err != nil {
		return err
	}
	return s.reserveUSD(ctx, cold, fmt.Sprintf("the next request is about %d tokens, up to $%s to write to the cache cold", size, strconv.FormatFloat(cold, 'f', 2, 64)))
}

func (s *state) reserveUSD(ctx context.Context, next float64, what string) error {
	spent, err := usage.TreeCost(ctx, s.d.Read, s.run.ID)
	if err != nil {
		return err
	}
	if spent+next > s.maxCost {
		return RunLimit(fmt.Sprintf("run cost cap: %s, with $%s of $%s already spent; raise %s to continue",
			what, strconv.FormatFloat(spent, 'f', 2, 64), strconv.FormatFloat(s.maxCost, 'f', 2, 64), budget.RunCapKey(s.run.Model)))
	}
	for parentID := s.run.ParentRunID; parentID != nil; {
		parent, err := journal.GetRun(ctx, s.d.Read, *parentID)
		if err != nil {
			return err
		}
		if parent == nil {
			return fmt.Errorf("loop: run %d: no parent run %d", s.run.ID, *parentID)
		}
		limit, err := runCapOf(ctx, s.d.Read, *parent)
		if err != nil {
			return err
		}
		spent, err := usage.TreeCost(ctx, s.d.Read, parent.ID)
		if err != nil {
			return err
		}
		if spent+next > limit {
			return RunLimit(fmt.Sprintf("parent run cost cap: %s, with $%s of $%s already spent by run %d and its subagents; raise %s to continue",
				what, strconv.FormatFloat(spent, 'f', 2, 64), strconv.FormatFloat(limit, 'f', 2, 64), parent.ID, budget.RunCapKey(parent.Model)))
		}
		parentID = parent.ParentRunID
	}
	threadSpent, limit, err := budget.ThreadCap(ctx, s.d.Read, s.thread.ID)
	if err != nil {
		return err
	}
	if threadSpent+next > limit {
		return RunLimit(fmt.Sprintf("thread cost cap: %s, with $%s of $%s already spent in this thread and its subagents; raise %s to continue",
			what, strconv.FormatFloat(threadSpent, 'f', 2, 64), strconv.FormatFloat(limit, 'f', 2, 64), budget.ThreadCapKey))
	}
	return nil
}

func runCapOf(ctx context.Context, q db.Querier, run journal.Run) (float64, error) {
	limits, err := journal.LimitsOf(ctx, q, run.ID)
	if err != nil {
		return 0, err
	}
	if limits.MaxCostUSD != nil {
		return *limits.MaxCostUSD, nil
	}
	return budget.RunCap(ctx, q, run.Model)
}

func (s *state) answer(ctx context.Context, uses []ToolUse) error {
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
	return s.repeat(uses, results)
}

func spanError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.As(err, new(LeaseLost)):
		return "lease_lost"
	case errors.As(err, new(RunLimit)):
		return "run_limit"
	case errors.As(err, new(budget.BudgetExceeded)):
		return "budget"
	case errors.As(err, new(*tool.Park)):
		return "parked"
	case errors.As(err, new(*anthropic.APIError)):
		return "api"
	case errors.Is(err, progress.ErrStopped):
		return "stopped"
	default:
		return "error"
	}
}

func genAIAttrs(span trace.Span, response *anthropic.Message, wall int64, cost float64, partial bool) {
	if response == nil {
		return
	}
	u := response.Usage
	span.SetAttributes(
		obs.Int64(obs.GenAIInputTokens, u.InputTokens),
		obs.Int64(obs.GenAIOutputTokens, u.OutputTokens),
		obs.Int64(obs.GenAICacheRead, u.CacheReadInputTokens),
		obs.Int64(obs.GenAICacheCreation, u.CacheCreationInputTokens),
		obs.String(obs.GenAIStopReason, response.StopReason),
		obs.Int64(obs.LLMWallMS, wall),
		obs.Float(obs.LLMCostUSD, cost),
		obs.Bool(obs.LLMPartial, partial),
	)
	total := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	if total > 0 {
		span.SetAttributes(obs.Float(obs.LLMCacheHitRatio, float64(u.CacheReadInputTokens)/float64(total)))
	}
}
