package serverops

import (
	"context"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
)

type source struct {
	env config.Env
}

func AttentionSource(env config.Env) attention.Source { return source{env: env} }

func (source) Name() string { return "ops" }

func (s source) Items(ctx context.Context, q db.Querier, now float64) ([]attention.Item, error) {
	budget, err := budgetItem(ctx, q, now, s.env)
	if err != nil || budget == nil {
		return nil, err
	}
	return []attention.Item{*budget}, nil
}

func budgetItem(ctx context.Context, q db.Querier, now float64, env config.Env) (*attention.Item, error) {
	set := assistant.Read(ctx, q)
	if err := set.Err(); err != nil {
		return nil, err
	}
	budget := set.Float(KeyWorkerMonthlyUSD)
	loc, err := config.From(ctx).Location(env)
	if err != nil {
		return nil, err
	}
	if budget <= 0 {
		if len(HostValues(ctx).Hosts) == 0 {
			return nil, nil
		}
	} else {
		refused, err := sourceCapRefused(ctx, q, now, loc)
		if err != nil || !refused {
			return nil, err
		}
	}
	spend, err := MonthSpend(ctx, q, now, loc)
	if err != nil {
		return nil, err
	}
	return &attention.Item{
		Kind: "ops_budget", SourceID: "ops", Title: "Ops worker budget spent",
		Detail: fmt.Sprintf("$%.2f of $%.2f this month", spend, budget),
		Group:  attention.NeedsDecision,
	}, nil
}

func sourceCapRefused(ctx context.Context, q db.Querier, now float64, loc *time.Location) (bool, error) {
	reason, err := journal.LatestClassificationError(ctx, q, string(classifier.Ops), inference.MonthStart(now, loc))
	return reason == classifier.ReasonSourceCap, err
}

func MonthSpend(ctx context.Context, q db.Querier, now float64, loc *time.Location) (float64, error) {
	return inference.SourceMonthCommitted(ctx, q, classifier.Ops.Source(), now, loc)
}
