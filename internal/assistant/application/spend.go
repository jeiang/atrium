package application

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/settings"
)

const SpendName application.Name = "assistant.spend"

const SpendTopRuns = 10

const (
	SpendOK        = "ok"
	SpendNear      = "near"
	SpendExhausted = "exhausted"
)

var spendScopes = []string{state.Assistant, state.Settings}

type SpendInput struct{}

type SpendRun struct {
	RunID        int64   `json:"run_id"`
	ThreadID     *int64  `json:"thread_id"`
	JobID        *int64  `json:"job_id"`
	Title        *string `json:"title"`
	Kind         string  `json:"kind"`
	Model        string  `json:"model"`
	StartedAt    float64 `json:"started_at"`
	Calls        int64   `json:"calls"`
	CostUSD      float64 `json:"cost_usd"`
	CommittedUSD float64 `json:"committed_usd"`
}

type SpendCap struct {
	Key      string  `json:"key"`
	Scope    string  `json:"scope"`
	Unit     string  `json:"unit"`
	Value    float64 `json:"value"`
	Source   string  `json:"source"`
	Editable bool    `json:"editable"`
}

type Spend struct {
	Month      string                 `json:"month"`
	MonthStart float64                `json:"month_start"`
	NextMonth  float64                `json:"next_month"`
	Budget     inference.Status       `json:"budget"`
	State      string                 `json:"state"`
	Headroom   []inference.Headroom   `json:"headroom"`
	Caps       []SpendCap             `json:"caps"`
	ByClass    []inference.SpendSlice `json:"by_class"`
	ByKind     []inference.SpendSlice `json:"by_kind"`
	ByModel    []inference.SpendSlice `json:"by_model"`
	ByDay      []inference.SpendDay   `json:"by_day"`
	TopRuns    []SpendRun             `json:"top_runs"`
	Topics     []string               `json:"topics"`
}

var spendCaps = []SpendCap{
	{Key: inference.BudgetKey, Scope: "month", Unit: "usd", Editable: true},
	{Key: inference.RunCapKey, Scope: "run", Unit: "usd", Editable: true},
	{Key: inference.ThreadCapKey, Scope: "thread", Unit: "usd"},
	{Key: inference.ChatKey, Scope: "chat_share", Unit: "share"},
	{Key: inference.UnattendedKey, Scope: "unattended_share", Unit: "share"},
	{Key: assistant.KeySubagentMaxCostUSD, Scope: "subagent_run", Unit: "usd"},
	{Key: KeySearchWebMaxCostUSD, Scope: "search_run", Unit: "usd"},
	{Key: assistant.KeyDreamingMaxCost, Scope: "dreaming_run", Unit: "usd"},
	{Key: assistant.KeyDreamingMonthly, Scope: "dreaming_month", Unit: "usd"},
	{Key: classifier.OpsRunCapKey, Scope: "ops_run", Unit: "usd"},
	{Key: classifier.OpsMonthlyCapKey, Scope: "ops_month", Unit: "usd"},
}

func DescribeSpend() application.Definition {
	return describeRead[SpendInput, Spend](SpendName)
}

func (s *Service) Spend(ctx context.Context, in SpendInput) (application.Read[Spend], error) {
	loc, err := config.From(ctx).Location(s.env)
	if err != nil {
		return application.Read[Spend]{}, err
	}
	now := s.now()
	out := Spend{
		Month: inference.MonthKey(now, loc), MonthStart: inference.MonthStart(now, loc), NextMonth: inference.NextMonthStart(now, loc),
		Topics: spendScopes,
	}
	bucket := struct {
		In    SpendInput
		Month string
	}{in, out.Month}
	rev, err := s.snapshot(ctx, string(SpendName), spendScopes, bucket, func(q db.Querier) error {
		var err error
		if out.Budget, err = inference.StatusAt(ctx, q, now, loc); err != nil {
			return err
		}
		out.Headroom = inference.HeadroomOf(out.Budget)
		out.State = SpendOK
		for _, h := range out.Headroom {
			switch {
			case h.Refused && h.Class == inference.Interactive:
				out.State = SpendExhausted
			case h.Refused && out.State == SpendOK:
				out.State = SpendNear
			}
		}
		if out.Caps, err = capsInForce(ctx, q); err != nil {
			return err
		}
		b, err := inference.MonthBreakdown(ctx, q, now, loc, SpendTopRuns)
		if err != nil {
			return err
		}
		out.ByClass, out.ByKind, out.ByModel, out.ByDay = b.ByClass, b.ByKind, b.ByModel, b.ByDay
		out.TopRuns, err = spendRuns(ctx, q, b.TopRuns)
		return err
	})
	return application.Read[Spend]{Revision: rev, Data: out}, err
}

func spendRuns(ctx context.Context, q db.Querier, runs []inference.RunSpend) ([]SpendRun, error) {
	out := make([]SpendRun, len(runs))
	for i, r := range runs {
		out[i] = SpendRun{RunID: r.RunID, ThreadID: r.ThreadID, Kind: r.Kind, Model: r.Model, StartedAt: r.StartedAt,
			Calls: r.Calls, CostUSD: r.CostUSD, CommittedUSD: r.CommittedUSD}
		err := q.QueryRowContext(ctx,
			"SELECT runs.thread_id, threads.job_id, threads.title FROM runs LEFT JOIN threads ON threads.id = runs.thread_id WHERE runs.id = ?",
			r.RunID).Scan(&out[i].ThreadID, &out[i].JobID, &out[i].Title)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return out, nil
}

func capsInForce(ctx context.Context, q db.Querier) ([]SpendCap, error) {
	reg := settings.From(ctx)
	out := make([]SpendCap, 0, len(spendCaps))
	for _, c := range spendCaps {
		if _, ok := reg.Lookup(c.Key); !ok {
			continue
		}
		found, err := reg.Resolve(ctx, q, c.Key)
		if err != nil {
			return nil, err
		}
		if c.Value, err = found.Float(); err != nil {
			return nil, err
		}
		c.Source = string(found.Origin)
		if found.Origin == settings.FromInstance {
			c.Source = "db"
		}
		out = append(out, c)
	}
	return out, nil
}
