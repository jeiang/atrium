package dreaming

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

type attentionSource struct{ env config.Env }

func Attention(env config.Env) attention.Source { return attentionSource{env: env} }

func (attentionSource) Name() string { return "dreaming" }

func (s attentionSource) Items(ctx context.Context, q db.Querier, now float64) ([]attention.Item, error) {
	loc, err := config.From(ctx).Location(s.env)
	if err != nil {
		return nil, err
	}
	r := assistant.Read(ctx, q)
	cap := r.Float(assistant.KeyDreamingMonthly)
	if err := r.Err(); err != nil {
		return nil, err
	}
	spent, err := monthlySpend(ctx, q, now, loc)
	if err != nil {
		return nil, err
	}
	if spent < cap {
		return nil, nil
	}
	return []attention.Item{{
		Kind: "dreaming_budget", SourceID: "monthly",
		Title:  "Dreaming paused: monthly cap reached",
		Detail: fmt.Sprintf("$%.2f of $%.2f spent", spent, cap),
		Group:  attention.NeedsDecision,
	}}, nil
}
