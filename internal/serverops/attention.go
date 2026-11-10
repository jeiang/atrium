package serverops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

type source struct{}

func AttentionSource() attention.Source { return source{} }

func (source) Name() string { return "ops" }

func (source) Items(ctx context.Context, q db.Querier, now float64) ([]attention.Item, error) {
	items, err := findingItems(ctx, q, now)
	if err != nil {
		return nil, err
	}
	budget, err := budgetItem(ctx, q, now)
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
		"SELECT id, title, aliases, origin, updated_at FROM kb_records WHERE scope = 'ops' AND origin LIKE 'finding:%' "+
			"AND retracted_at IS NULL AND (expires_at IS NULL OR expires_at > ?) ORDER BY updated_at DESC LIMIT 20", now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []attention.Item
	for rows.Next() {
		var id int64
		var title, aliases, origin string
		var written float64
		if err := rows.Scan(&id, &title, &aliases, &origin, &written); err != nil {
			return nil, err
		}
		severity := ""
		var list []string
		if err := json.Unmarshal([]byte(aliases), &list); err == nil {
			for _, alias := range list {
				if strings.HasPrefix(alias, "severity:") {
					severity = strings.TrimPrefix(alias, "severity:")
				}
			}
		}
		if severity != "warning" && severity != "critical" {
			continue
		}
		out = append(out, attention.Item{
			Kind: "ops_finding", SourceID: fmt.Sprint(id), Title: title,
			Detail:  severity + " · " + strings.TrimPrefix(origin, "finding:"),
			Created: written, Group: attention.NeedsDecision,
		})
	}
	return out, rows.Err()
}

func budgetItem(ctx context.Context, q db.Querier, now float64) (*attention.Item, error) {
	set := assistant.Read(ctx, q)
	if err := set.Err(); err != nil {
		return nil, err
	}
	budget := set.Float(KeyWorkerMonthlyUSD)
	threadID, err := WorkerThreadID(ctx, q)
	if err != nil || threadID == 0 {
		return nil, err
	}
	loc := time.UTC
	spend, err := usage.ThreadMonthToDate(ctx, q, threadID, now, loc)
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
