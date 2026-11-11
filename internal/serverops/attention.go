package serverops

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant"
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
	items, err := findingItems(ctx, q, now)
	if err != nil {
		return nil, err
	}
	budget, err := budgetItem(ctx, q, now, s.env)
	if err != nil {
		return nil, err
	}
	if budget != nil {
		items = append(items, *budget)
	}
	return items, nil
}

func findingItems(ctx context.Context, q db.Querier, now float64) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, fact, severity, title, created_at FROM ops_findings WHERE resolved_at IS NULL "+
			"AND severity IN ('warning', 'critical') ORDER BY created_at DESC, id DESC LIMIT 20")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []attention.Item
	for rows.Next() {
		var id int64
		var fact, severity, title string
		var written float64
		if err := rows.Scan(&id, &fact, &severity, &title, &written); err != nil {
			return nil, err
		}
		out = append(out, attention.Item{
			Kind: "ops_finding", SourceID: fmt.Sprint(id), Title: title,
			Detail:  severity + " · " + fact,
			Created: written, Group: attention.NeedsDecision,
		})
	}
	return out, rows.Err()
}

func budgetItem(ctx context.Context, q db.Querier, now float64, env config.Env) (*attention.Item, error) {
	set := assistant.Read(ctx, q)
	if err := set.Err(); err != nil {
		return nil, err
	}
	if set.Text(KeyCondenser) == CondenserLocal {
		return nil, nil
	}
	budget := set.Float(KeyWorkerMonthlyUSD)
	threadID, err := WorkerThreadID(ctx, q)
	if err != nil || threadID == 0 {
		return nil, err
	}
	loc, err := config.From(ctx).Location(env)
	if err != nil {
		return nil, err
	}
	spend, err := inference.ThreadMonthToDate(ctx, q, threadID, now, loc)
	if err != nil {
		return nil, err
	}
	if spend < budget {
		return nil, nil
	}
	return &attention.Item{
		Kind: "ops_budget", SourceID: "ops", Title: "Ops worker budget spent",
		Detail: fmt.Sprintf("$%.2f of $%.2f this month", spend, budget),
		Group:  attention.NeedsDecision,
	}, nil
}
