package inference

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/obs"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tokens"
)

type Class string

const (
	Interactive       Class = "interactive"
	Unattended        Class = "unattended"
	Classification    Class = "classification"
	ClassSubscription Class = "subscription"
)

const ClassificationReserveShare = 0.05

const (
	UnknownExpiry  = 24 * time.Hour
	RequestTimeout = 15 * time.Minute
)

type Request struct {
	AttemptKey string
	RunID      int64
	ThreadID   int64
	Kind       string
	Source     string
	JobName    string
	Effort     string
	Class      Class
	CacheTTL   string
	Params     anthropic.Params
}

type RunScope struct {
	ID         int64
	Model      string
	MaxCostUSD *float64
}

type ThreadScope struct {
	RootID int64
	CapUSD *float64
	IDs    []int64
}

type Scopes interface {
	RunAncestors(context.Context, db.Querier, int64) ([]RunScope, error)
	RunDescendants(context.Context, db.Querier, int64) ([]int64, error)
	RunThread(context.Context, db.Querier, int64) (int64, error)
	ThreadTree(context.Context, db.Querier, int64) (ThreadScope, error)
}

type Service struct {
	db     *db.DB
	now    func() float64
	loc    *time.Location
	scopes Scopes
}

func New(d *db.DB, now func() float64, loc *time.Location, scopes Scopes) *Service {
	if now == nil {
		now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	}
	return &Service{db: d, now: now, loc: loc, scopes: scopes}
}

type Outcome struct {
	Message            *anthropic.Message
	Started            bool
	Complete           bool
	DefinitelyUnsent   bool
	DefinitelyRejected bool
	WallMS             int64
	Err                error

	usage anthropic.Usage
	known bool
}

type Settlement struct {
	Outcome    Outcome
	ToolCalls  int64
	Compaction int64
}

type Receipt struct {
	ID           int64
	State        string
	ActualUSD    float64
	CommittedUSD float64
	Unpriced     bool
}

type Attempt struct {
	id          int64
	key         string
	params      anthropic.Params
	class       Class
	model       string
	kind        string
	reservation int64
	expiresAt   float64
	deadline    float64
	now         func() float64
	consumed    atomic.Bool
}

func NewAttemptKey(prefix string) string {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		panic(fmt.Sprintf("inference: attempt key: %v", err))
	}
	return prefix + ":" + hex.EncodeToString(suffix[:])
}

type prepared struct {
	model        string
	maxTokens    int64
	searches     int64
	promptTokens int64
	reservation  int64
}

func (s *Service) Reserve(ctx context.Context, r Request) (*Attempt, error) {
	p, err := prepare(r)
	if err != nil {
		return nil, err
	}
	if s.loc == nil {
		return nil, errors.New("inference: no configured timezone")
	}
	now := s.now()
	month := MonthStart(now, s.loc)
	expires := now + UnknownExpiry.Seconds()
	deadline := now + RequestTimeout.Seconds()
	var id int64
	err = s.db.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE usage SET expires_at = expires_at WHERE id = -1"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE usage SET state = 'expired', accounted_nusd = reservation_nusd, settled_at = ? WHERE state IN ('reserved','unknown') AND expires_at <= ?",
			now, now); err != nil {
			return err
		}
		if err := s.admit(ctx, tx, r, p, now, month); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO usage (attempt_key, ts, month_start, expires_at, state, budget_class, reservation_nusd, accounted_nusd, "+
				"estimated_prompt_tokens, max_output_tokens, max_searches, cache_ttl, request_id, source, provider, thread_id, run_id, "+
				"kind, job_name, model, effort, iterations) "+
				"VALUES (?, ?, ?, ?, 'reserved', ?, ?, 0, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, '[]')",
			r.AttemptKey, now, month, expires, classOf(r), p.reservation, p.promptTokens, p.maxTokens, p.searches,
			cacheTTLOf(r), sourceOf(r), ProviderAnthropic, r.ThreadID, r.RunID, kindOf(r), nullableText(r.JobName), p.model, r.Effort)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return nil, err
	}
	return &Attempt{
		id: id, key: r.AttemptKey, params: r.Params, class: classOf(r), model: p.model, kind: kindOf(r),
		reservation: p.reservation, expiresAt: expires, deadline: deadline, now: s.now,
	}, nil
}

func (s *Service) admit(ctx context.Context, tx *sql.Tx, r Request, p prepared, now, month float64) error {
	if r.RunID <= 0 {
		return errors.New("inference: a reservation needs a run")
	}
	if r.ThreadID <= 0 {
		return errors.New("inference: a reservation needs a thread")
	}
	threadID, err := s.scopes.RunThread(ctx, tx, r.RunID)
	if err != nil {
		return err
	}
	if threadID != r.ThreadID {
		return fmt.Errorf("inference: run %d belongs to thread %d, not thread %d", r.RunID, threadID, r.ThreadID)
	}
	ancestors, err := s.scopes.RunAncestors(ctx, tx, r.RunID)
	if err != nil {
		return err
	}
	if len(ancestors) == 0 || ancestors[0].ID != r.RunID {
		return fmt.Errorf("inference: run %d has no accounting scope", r.RunID)
	}
	for _, scope := range ancestors {
		capUSD := 0.0
		if scope.MaxCostUSD != nil {
			capUSD = *scope.MaxCostUSD
		} else if capUSD, err = RunCap(ctx, tx, scope.Model); err != nil {
			return err
		}
		ids, err := s.scopes.RunDescendants(ctx, tx, scope.ID)
		if err != nil {
			return err
		}
		committed, err := CommittedOnRuns(ctx, tx, ids)
		if err != nil {
			return err
		}
		if committed+p.reservation > nanoUSD(capUSD) {
			reason, what := ReasonRun, "run cost cap"
			if scope.ID != r.RunID {
				reason, what = ReasonParentRun, "parent run cost cap"
			}
			return BudgetExceeded{
				Text: fmt.Sprintf("%s: the next request would reserve $%s, with $%s of $%s already committed; raise %s to continue",
					what, strconvUSD(usdOf(p.reservation)), strconvUSD(usdOf(committed)), strconvUSD(capUSD), RunCapKey(scope.Model)),
				Reason: reason, Class: classOf(r), NextMonth: NextMonthStart(now, s.loc),
			}
		}
	}
	tree, err := s.scopes.ThreadTree(ctx, tx, r.ThreadID)
	if err != nil {
		return err
	}
	spent, limit, own, err := threadCapN(ctx, tx, tree)
	if err != nil {
		return err
	}
	if spent+p.reservation > limit {
		remedy := "raise " + ThreadCapKey + " to continue"
		if own {
			remedy = "this is the cost cap chosen when the thread was created; start a new thread to continue"
		}
		return BudgetExceeded{
			Text: fmt.Sprintf("thread cost cap: the next request would reserve $%s, with $%s of $%s already committed in this thread and its subagents; %s",
				strconvUSD(usdOf(p.reservation)), strconvUSD(usdOf(spent)), strconvUSD(usdOf(limit)), remedy),
			Reason: ReasonThread, Class: classOf(r), NextMonth: NextMonthStart(now, s.loc),
		}
	}
	budget, err := MonthlyBudget(ctx, tx)
	if err != nil {
		return err
	}
	chat, err := chatShare(ctx, tx)
	if err != nil {
		return err
	}
	unattended, err := unattendedShare(ctx, tx)
	if err != nil {
		return err
	}
	limitUSD := budget
	switch classOf(r) {
	case Classification:
		limitUSD = budget
	case Unattended:
		limitUSD = ceiling(budget, unattended)
	default:
		limitUSD = ceiling(budget, chat)
	}
	var committed, carry int64
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(SUM("+committedSQL+"), 0) FROM usage WHERE month_start = ? AND budget_class <> ?",
		month, ClassSubscription).Scan(&committed); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(reservation_nusd), 0) FROM usage WHERE month_start < ? AND state IN ('reserved','unknown') "+
			"AND expires_at > ? AND budget_class <> ?", month, now, ClassSubscription).Scan(&carry); err != nil {
		return err
	}
	if committed+carry+p.reservation > nanoUSD(limitUSD) {
		return BudgetExceeded{
			Text: fmt.Sprintf("monthly budget: $%s of $%s committed (%s); the next request would reserve $%s",
				strconvUSD(usdOf(committed+carry)), strconvUSD(budget), strconvPercent(usdOf(committed+carry)/budget), strconvUSD(usdOf(p.reservation))),
			Reason: ReasonMonthly, Class: classOf(r), NextMonth: NextMonthStart(now, s.loc),
		}
	}
	return nil
}

func prepare(r Request) (prepared, error) {
	if strings.TrimSpace(r.AttemptKey) == "" {
		return prepared{}, errors.New("inference: a reservation needs an attempt key")
	}
	switch classOf(r) {
	case Interactive, Unattended, Classification:
	default:
		return prepared{}, fmt.Errorf("inference: unknown budget class %q", r.Class)
	}
	model, err := pricing.Lookup(r.Params.Model)
	if err != nil {
		return prepared{}, err
	}
	if r.Params.MaxTokens <= 0 || r.Params.MaxTokens > model.MaxOutput {
		return prepared{}, fmt.Errorf("inference: max tokens %d is outside 1..%d for %s", r.Params.MaxTokens, model.MaxOutput, model.ID)
	}
	if len(r.Params.Messages) == 0 {
		return prepared{}, errors.New("inference: a reservation needs at least one message")
	}
	switch r.CacheTTL {
	case "", prompts.TTL5m, prompts.TTL1h:
	default:
		return prepared{}, fmt.Errorf("inference: unknown cache TTL %q", r.CacheTTL)
	}
	searches, err := searchUses(r.Params.Tools)
	if err != nil {
		return prepared{}, err
	}
	if classOf(r) == Classification {
		if model.ID != pricing.HaikuID {
			return prepared{}, errors.New("inference: classification requires the Haiku model")
		}
		if len(r.Params.Tools) > 0 {
			return prepared{}, errors.New("inference: classification may not use tools")
		}
	}
	prompt, err := estimatePrompt(r.Params)
	if err != nil {
		return prepared{}, err
	}
	reservation, err := reservationNUSD(model.ID, prompt, r.Params.MaxTokens, searches, cacheTTLOf(r))
	if err != nil {
		return prepared{}, err
	}
	return prepared{model: model.ID, maxTokens: r.Params.MaxTokens, searches: searches, promptTokens: prompt, reservation: reservation}, nil
}

func classOf(r Request) Class {
	if r.Class == "" {
		return Interactive
	}
	return r.Class
}

func sourceOf(r Request) string {
	if r.Source == "" {
		return SourceLoop
	}
	return r.Source
}

func kindOf(r Request) string {
	if r.Kind == "" {
		return "unknown"
	}
	return r.Kind
}

func cacheTTLOf(r Request) string {
	switch r.CacheTTL {
	case prompts.TTL1h:
		return prompts.TTL1h
	default:
		return prompts.TTL5m
	}
}

func estimatePrompt(p anthropic.Params) (int64, error) {
	fixed := map[string]any{}
	if len(p.System) > 0 {
		system, err := jsonx.Loads(p.System)
		if err != nil {
			return 0, err
		}
		fixed["system"] = system
	}
	if len(p.Tools) > 0 {
		list := make([]any, len(p.Tools))
		for i, raw := range p.Tools {
			value, err := jsonx.Loads(raw)
			if err != nil {
				return 0, err
			}
			list[i] = value
		}
		fixed["tools"] = list
	}
	total := tokens.EstimateValue(fixed)
	for _, raw := range p.Messages {
		var message struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return 0, err
		}
		if len(message.Content) == 0 {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			total += tokens.Estimate(text)
			continue
		}
		var blocks []json.RawMessage
		if err := json.Unmarshal(message.Content, &blocks); err != nil {
			return 0, err
		}
		n, err := tokens.EstimateContent(blocks)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func searchUses(tools []json.RawMessage) (int64, error) {
	var total int64
	for _, raw := range tools {
		var tool struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			MaxUses *int64 `json:"max_uses"`
		}
		if err := json.Unmarshal(raw, &tool); err != nil {
			return 0, err
		}
		if !strings.HasPrefix(tool.Type, "web_search") && tool.Name != "web_search" {
			continue
		}
		if tool.MaxUses == nil || *tool.MaxUses <= 0 {
			return 0, fmt.Errorf("inference: the web search tool %q has no finite max_uses", tool.Name)
		}
		total += *tool.MaxUses
	}
	return total, nil
}

func reservationNUSD(model string, prompt, maxTokens, searches int64, ttl string) (int64, error) {
	t := pricing.Tokens{Output: maxTokens}
	switch ttl {
	case prompts.TTL1h:
		t.CacheWrite1h = prompt
	case prompts.TTL5m:
		t.CacheWrite5m = prompt
	default:
		return 0, fmt.Errorf("inference: unknown cache TTL %q", ttl)
	}
	cost, err := pricing.CostUSD(model, t, searches)
	if err != nil {
		return 0, err
	}
	return nanoUSD(cost), nil
}

func threadCapN(ctx context.Context, q db.Querier, scope ThreadScope) (spent, limit int64, own bool, err error) {
	found, err := settings.From(ctx).Resolve(ctx, q, ThreadCapKey)
	if err != nil {
		return 0, 0, false, err
	}
	capUSD, err := found.Float()
	if err != nil {
		return 0, 0, false, err
	}
	limit = nanoUSD(capUSD)
	spent, err = CommittedOnThreads(ctx, q, scope.IDs)
	if err != nil {
		return 0, 0, false, err
	}
	if scope.CapUSD != nil && nanoUSD(*scope.CapUSD) < limit {
		limit, own = nanoUSD(*scope.CapUSD), true
	}
	return spent, limit, own, nil
}

func (a *Attempt) Stream(ctx context.Context, c *anthropic.Client, observe func(anthropic.Event) error) Outcome {
	if !a.consumed.CompareAndSwap(false, true) {
		return Outcome{DefinitelyUnsent: true, Err: errors.New("inference: the attempt was already dispatched")}
	}
	now := a.now()
	if now >= a.deadline {
		return Outcome{DefinitelyUnsent: true, Err: errors.New("inference: the attempt deadline passed before dispatch")}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration((a.deadline-now)*float64(time.Second)))
	defer cancel()
	started := time.Now()
	return outcomeOf(c.Stream(ctx, a.params, observe), time.Since(started).Milliseconds())
}

func (a *Attempt) Create(ctx context.Context, c *anthropic.Client) Outcome {
	if !a.consumed.CompareAndSwap(false, true) {
		return Outcome{DefinitelyUnsent: true, Err: errors.New("inference: the attempt was already dispatched")}
	}
	now := a.now()
	if now >= a.deadline {
		return Outcome{DefinitelyUnsent: true, Err: errors.New("inference: the attempt deadline passed before dispatch")}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration((a.deadline-now)*float64(time.Second)))
	defer cancel()
	started := time.Now()
	return outcomeOf(c.Create(ctx, a.params), time.Since(started).Milliseconds())
}

func outcomeOf(transport anthropic.Outcome, wall int64) Outcome {
	return Outcome{
		Message:            transport.Message,
		Started:            transport.Started,
		Complete:           transport.Complete,
		DefinitelyUnsent:   transport.DefinitelyUnsent,
		DefinitelyRejected: transport.DefinitelyRejected,
		WallMS:             wall,
		Err:                transport.Err,
		usage:              transport.Usage,
		known:              true,
	}
}

func (s *Service) Settle(ctx context.Context, a *Attempt, st Settlement) (Receipt, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	o := st.Outcome
	switch {
	case o.Complete && o.Err == nil:
		return s.settle(ctx, a, st, false, true)
	case o.Started:
		return s.settle(ctx, a, st, true, true)
	case o.DefinitelyUnsent || o.DefinitelyRejected:
		return s.settle(ctx, a, st, false, false)
	default:
		return s.markUnknown(ctx, a, o)
	}
}

func (s *Service) settle(ctx context.Context, a *Attempt, st Settlement, partial, actual bool) (Receipt, error) {
	o := st.Outcome
	var p priced
	if actual {
		usage := o.usage
		if !o.known && o.Message != nil {
			usage = o.Message.Usage
		}
		p = priceUsage(a.model, usage)
	}
	var requestID any
	if o.Message != nil && o.Message.ID != "" {
		requestID = o.Message.ID
	}
	var errorText any
	if o.Err != nil {
		errorText = o.Err.Error()
	}
	iterations := "[]"
	if actual {
		raw, err := json.Marshal(p.iterations)
		if err != nil {
			return Receipt{}, err
		}
		iterations = string(raw)
	}
	var clearEdits, clearedTokens int64
	if o.Message != nil && o.Message.ContextManagement != nil {
		clearEdits = int64(len(o.Message.ContextManagement.AppliedEdits))
		for _, edit := range o.Message.ContextManagement.AppliedEdits {
			clearedTokens += edit.ClearedInputTokens
		}
	}
	accounted := int64(0)
	if actual {
		accounted = nanoUSD(p.cost)
	}
	var receipt Receipt
	recorded := false
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var state string
		var existingRequest sql.NullString
		var existingAccounted, existingPartial int64
		var existingCost float64
		var existingUnpriced int64
		err := tx.QueryRowContext(ctx, "SELECT state, request_id, accounted_nusd, partial, cost_usd, unpriced FROM usage WHERE id = ?", a.id).
			Scan(&state, &existingRequest, &existingAccounted, &existingPartial, &existingCost, &existingUnpriced)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("inference: no ledger row %d", a.id)
		}
		if err != nil {
			return err
		}
		switch state {
		case "settled":
			if sameRequest(existingRequest, requestID) && existingAccounted == accounted && existingPartial == boolInt(partial) {
				receipt = Receipt{ID: a.id, State: state, ActualUSD: existingCost, CommittedUSD: usdOf(existingAccounted), Unpriced: existingUnpriced != 0}
				return nil
			}
			return fmt.Errorf("inference: ledger row %d is already settled with a different outcome", a.id)
		case "expired":
			if !actual {
				return fmt.Errorf("inference: ledger row %d expired and cannot be settled without an actual response", a.id)
			}
		case "reserved", "unknown":
		default:
			return fmt.Errorf("inference: ledger row %d is in state %q", a.id, state)
		}
		_, err = tx.ExecContext(ctx,
			"UPDATE usage SET state = 'settled', settled_at = ?, request_id = ?, iterations = ?, input_tokens = ?, cache_write_5m = ?, "+
				"cache_write_1h = ?, cache_read = ?, output_tokens = ?, reasoning_tokens = ?, searches = ?, tool_calls = ?, wall_ms = ?, "+
				"tier = ?, cost_usd = ?, partial = ?, unpriced = ?, clear_edits = ?, cleared_tokens = ?, compaction = ?, accounted_nusd = ?, "+
				"error = ? WHERE id = ?",
			s.now(), requestID, iterations, p.tokens.Input, p.tokens.CacheWrite5m, p.tokens.CacheWrite1h, p.tokens.CacheRead,
			p.tokens.Output, p.reasoning, p.searches, st.ToolCalls, o.WallMS, p.tier, p.cost, boolInt(partial), p.unpriced,
			clearEdits, clearedTokens, st.Compaction, accounted, errorText, a.id)
		if err != nil {
			return err
		}
		receipt = Receipt{ID: a.id, State: "settled", ActualUSD: p.cost, CommittedUSD: usdOf(accounted), Unpriced: p.unpriced}
		recorded = true
		return nil
	})
	if err == nil && recorded && actual {
		emitUsage(a.model, a.kind, partial, o.WallMS, p)
	}
	return receipt, err
}

func emitUsage(model, kind string, partial bool, wallMS int64, p priced) {
	if kind == "" {
		kind = "unknown"
	}
	if model == "" {
		model = "unknown"
	}
	obs.LLMRequests.Add(1, model, kind, strconv.FormatBool(partial))
	obs.LLMDuration.Observe(float64(wallMS)/1000, model, kind)
	obs.LLMTokens.Add(float64(p.tokens.Input), model, kind, "input")
	obs.LLMTokens.Add(float64(p.tokens.CacheWrite5m+p.tokens.CacheWrite1h), model, kind, "cache_write")
	obs.LLMTokens.Add(float64(p.tokens.CacheRead), model, kind, "cache_read")
	obs.LLMTokens.Add(float64(p.tokens.Output), model, kind, "output")
	obs.LLMTokens.Add(float64(p.reasoning), model, kind, "reasoning")
	obs.LLMCost.Add(p.cost, model, kind)
}

func (s *Service) markUnknown(ctx context.Context, a *Attempt, o Outcome) (Receipt, error) {
	var errorText any
	if o.Err != nil {
		errorText = o.Err.Error()
	}
	var receipt Receipt
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var state string
		var reservation int64
		err := tx.QueryRowContext(ctx, "SELECT state, reservation_nusd FROM usage WHERE id = ?", a.id).Scan(&state, &reservation)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("inference: no ledger row %d", a.id)
		}
		if err != nil {
			return err
		}
		switch state {
		case "reserved":
			if _, err := tx.ExecContext(ctx, "UPDATE usage SET state = 'unknown', error = ? WHERE id = ?", errorText, a.id); err != nil {
				return err
			}
		case "unknown":
		case "settled", "expired":
			return fmt.Errorf("inference: ledger row %d is already %s", a.id, state)
		default:
			return fmt.Errorf("inference: ledger row %d is in state %q", a.id, state)
		}
		receipt = Receipt{ID: a.id, State: "unknown", CommittedUSD: usdOf(reservation)}
		return nil
	})
	return receipt, err
}

func sameRequest(existing sql.NullString, current any) bool {
	if !existing.Valid {
		return current == nil
	}
	text, ok := current.(string)
	return ok && existing.String == text
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func strconvUSD(v float64) string { return fmt.Sprintf("%.2f", v) }

func strconvPercent(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }
