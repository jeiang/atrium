package classifier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/jsonx"
)

const (
	ReasonBudget      = "budget"
	ReasonRunCap      = "run_cap"
	ReasonSourceCap   = "source_cap"
	ReasonUnavailable = "unavailable"
	ReasonAuth        = "auth"
	ReasonSpendCap    = "spend_cap"
	ReasonRefusal     = "refusal"
	ReasonTruncated   = "truncated"
	ReasonStop        = "unexpected_stop"
	ReasonMalformed   = "malformed_output"
	ReasonExhausted   = "exhausted"
	ReasonRetryLater  = "retry_later"
)

const MaxItemFailures = 2

const RetrySpacing = time.Hour

const finishTimeout = 5 * time.Second

type Item struct {
	Key string
}

type Request struct {
	Caller       Caller
	Item         Item
	System, User string
	Schema       json.RawMessage
}

type Deferred struct {
	Caller  Caller
	ItemKey string
	Reason  string
	Cause   error
}

func (e *Deferred) Error() string {
	text := fmt.Sprintf("classifier: %s item %s deferred: %s", e.Caller, e.ItemKey, e.Reason)
	if e.Cause != nil {
		text += ": " + e.Cause.Error()
	}
	return text
}

func (e *Deferred) Unwrap() error { return e.Cause }

type Unanswered struct {
	Caller  Caller
	ItemKey string
	Reason  string
	Final   bool
	Cause   error
}

func (e *Unanswered) Error() string {
	text := fmt.Sprintf("classifier: %s item %s unanswered: %s", e.Caller, e.ItemKey, e.Reason)
	if e.Cause != nil {
		text += ": " + e.Cause.Error()
	}
	return text
}

func (e *Unanswered) Unwrap() error { return e.Cause }

func (c Caller) Source() string { return "classifier:" + string(c) }

type Service struct {
	db     *db.DB
	ledger *inference.Service
	client *anthropic.Client
	now    func() float64
}

func New(d *db.DB, ledger *inference.Service, client *anthropic.Client, now func() float64) *Service {
	return &Service{db: d, ledger: ledger, client: client, now: now}
}

func (s *Service) Classify(ctx context.Context, r Request) (json.RawMessage, error) {
	def, ok := definitions[r.Caller]
	if !ok {
		return nil, fmt.Errorf("classifier: unknown caller %q", r.Caller)
	}
	if r.Item.Key == "" || strings.TrimSpace(r.System) == "" || strings.TrimSpace(r.User) == "" {
		return nil, fmt.Errorf("classifier: %s needs an item key, a system prompt and a user prompt", r.Caller)
	}
	wire, err := def.wire(r.Caller, r.Schema)
	if err != nil {
		return nil, err
	}
	if def.reuse {
		kept, err := s.kept(ctx, r)
		if err != nil || kept != nil {
			return kept, err
		}
	}
	failures, last, err := s.itemFailures(ctx, r)
	if err != nil {
		return nil, err
	}
	if failures >= MaxItemFailures {
		return nil, &Unanswered{Caller: r.Caller, ItemKey: r.Item.Key, Reason: ReasonExhausted, Final: true}
	}
	if failures > 0 && !def.ops && s.now()-last < RetrySpacing.Seconds() {
		return nil, &Unanswered{Caller: r.Caller, ItemKey: r.Item.Key, Reason: ReasonRetryLater}
	}
	if s.client == nil {
		return nil, &Deferred{Caller: r.Caller, ItemKey: r.Item.Key, Reason: ReasonUnavailable,
			Cause: config.NotConfigured("the Anthropic connection is not configured")}
	}
	limits, err := def.caps(ctx, s.db.Read)
	if err != nil {
		return nil, err
	}
	runID, threadID, err := journal.StartClassification(ctx, s.db, string(r.Caller), r.Item.Key, s.now())
	if err != nil {
		return nil, err
	}
	out, reason, err := s.run(ctx, r, runID, threadID, wire, def.maxTokens, limits)
	if err == nil && def.reuse {
		if _, keepErr := journal.AppendMessage(ctx, s.db, threadID, &runID, "assistant",
			[]json.RawMessage{json.RawMessage(jsonx.Dumps(map[string]any{"type": "text", "text": string(out)}))}, s.now(), nil); keepErr != nil {
			out, reason, err = nil, "storage", keepErr
		}
	}
	var unanswered *Unanswered
	if errors.As(err, &unanswered) {
		unanswered.Final = failures+1 >= MaxItemFailures
	}
	state := journal.Done
	if err != nil {
		state = journal.Failed
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if finishErr := journal.FinishClassification(finishCtx, s.db.Write, runID, state, s.now(), reason); finishErr != nil {
		return nil, fmt.Errorf("classifier: %s item %s: closing the accounting run: %w", r.Caller, r.Item.Key, finishErr)
	}
	return out, err
}

const classificationRuns = "FROM runs r JOIN threads t ON t.id = r.thread_id " +
	"WHERE t.kind = 'classification' AND t.role = ? AND r.kind = 'classification' AND r.state = ? " +
	"AND json_extract(r.limits_json, '$.item_key') = ?"

func (s *Service) itemFailures(ctx context.Context, r Request) (int, float64, error) {
	var n int
	var last float64
	err := s.db.Read.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(MAX(r.started_at), 0) "+classificationRuns+" AND r.error IN (?, ?, ?, ?)",
		"classification:"+string(r.Caller), journal.Failed, r.Item.Key, ReasonRefusal, ReasonTruncated, ReasonStop, ReasonMalformed).Scan(&n, &last)
	return n, last, err
}

func (s *Service) kept(ctx context.Context, r Request) (json.RawMessage, error) {
	rows, err := s.db.Read.QueryContext(ctx, "SELECT m.content_json FROM thread_messages m JOIN runs r ON r.id = m.run_id "+
		strings.TrimPrefix(classificationRuns, "FROM runs r ")+" AND m.role = 'assistant' ORDER BY m.id DESC",
		"classification:"+string(r.Caller), journal.Done, r.Item.Key)
	if err != nil {
		return nil, err
	}
	stored, err := db.Collect(rows, func(rows *sql.Rows) (string, error) {
		var content string
		err := rows.Scan(&content)
		return content, err
	})
	if err != nil {
		return nil, err
	}
	for _, content := range stored {
		var blocks []json.RawMessage
		if json.Unmarshal([]byte(content), &blocks) != nil {
			continue
		}
		text, err := onlyText(blocks)
		if err == nil && anthropic.ValidateJSONOutput(r.Schema, json.RawMessage(text)) == nil {
			return json.RawMessage(text), nil
		}
	}
	return nil, nil
}

func (s *Service) run(ctx context.Context, r Request, runID, threadID int64, wire json.RawMessage, maxTokens int64, limits caps) (json.RawMessage, string, error) {
	if limits.runUSD != nil {
		if err := journal.SetLimits(ctx, s.db.Write, runID, journal.Limits{MaxCostUSD: limits.runUSD, ItemKey: r.Item.Key}); err != nil {
			return nil, "storage", err
		}
	}
	source := r.Caller.Source()
	attempt, err := s.ledger.Reserve(ctx, inference.Request{
		AttemptKey: inference.NewAttemptKey(source),
		RunID:      runID,
		ThreadID:   threadID,
		Kind:       "classification",
		Source:     source,
		Class:      inference.Classification,
		Params: anthropic.Params{
			Model:     pricing.HaikuID,
			MaxTokens: maxTokens,
			System:    json.RawMessage(jsonx.Dumps(r.System)),
			OutputConfig: &anthropic.OutputConfig{Format: &anthropic.JSONOutputFormat{
				Type: anthropic.FormatJSONSchema, Schema: wire,
			}},
			Messages: []json.RawMessage{json.RawMessage(jsonx.Dumps(map[string]any{"role": "user", "content": r.User}))},
		},
		SourceMonthlyCapUSD: limits.monthlyUSD,
	})
	var refused inference.BudgetExceeded
	if errors.As(err, &refused) {
		return s.refused(ctx, r, refused)
	}
	if err != nil {
		return nil, "reserve", err
	}
	outcome := attempt.Create(ctx, s.client)
	if _, err := s.ledger.Settle(ctx, attempt, inference.Settlement{Outcome: outcome}); err != nil {
		return nil, "settlement", err
	}
	if outcome.Err != nil {
		if ctx.Err() != nil {
			return nil, "canceled", ctx.Err()
		}
		reason := transportReason(outcome.Err)
		return nil, reason, &Deferred{Caller: r.Caller, ItemKey: r.Item.Key, Reason: reason, Cause: outcome.Err}
	}
	msg := outcome.Message
	if msg == nil {
		return nil, ReasonUnavailable, &Deferred{Caller: r.Caller, ItemKey: r.Item.Key, Reason: ReasonUnavailable}
	}
	switch msg.StopReason {
	case "end_turn":
	case "refusal":
		return nil, ReasonRefusal, &Unanswered{Caller: r.Caller, ItemKey: r.Item.Key, Reason: ReasonRefusal}
	case "max_tokens":
		return nil, ReasonTruncated, &Unanswered{Caller: r.Caller, ItemKey: r.Item.Key, Reason: ReasonTruncated}
	default:
		return nil, ReasonStop, &Unanswered{Caller: r.Caller, ItemKey: r.Item.Key, Reason: ReasonStop}
	}
	text, err := onlyText(msg.Content)
	if err == nil {
		err = anthropic.ValidateJSONOutput(r.Schema, json.RawMessage(text))
	}
	if err != nil {
		return nil, ReasonMalformed, &Unanswered{Caller: r.Caller, ItemKey: r.Item.Key, Reason: ReasonMalformed, Cause: err}
	}
	return json.RawMessage(text), "", nil
}

func (s *Service) refused(ctx context.Context, r Request, refused inference.BudgetExceeded) (json.RawMessage, string, error) {
	reason := ReasonRunCap
	switch refused.Reason {
	case inference.ReasonMonthly:
		reason = ReasonBudget
		loc, err := inference.Location(ctx, s.db.Read)
		if err != nil {
			return nil, "storage", err
		}
		if err := s.ledger.RecordClassificationDeferral(ctx, inference.MonthStart(s.now(), loc), string(r.Caller), r.Item.Key); err != nil {
			return nil, "storage", err
		}
	case inference.ReasonSourceMonth:
		reason = ReasonSourceCap
	}
	return nil, reason, &Deferred{Caller: r.Caller, ItemKey: r.Item.Key, Reason: reason, Cause: refused}
}

func transportReason(err error) string {
	var api *anthropic.APIError
	switch {
	case !errors.As(err, &api):
		return ReasonUnavailable
	case api.SpendCapped():
		return ReasonSpendCap
	case api.StatusCode == http.StatusUnauthorized || api.StatusCode == http.StatusForbidden:
		return ReasonAuth
	default:
		return ReasonUnavailable
	}
}

func onlyText(content []json.RawMessage) (string, error) {
	if len(content) != 1 {
		return "", fmt.Errorf("reply has %d content blocks, want one text block", len(content))
	}
	var b struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content[0], &b); err != nil {
		return "", err
	}
	if b.Type != "text" {
		return "", fmt.Errorf("reply block is %q, want text", b.Type)
	}
	return b.Text, nil
}
