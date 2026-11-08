package nightly

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/settings"
)

const StaleMonthsKey = "nightly_stale_months"

func init() {
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: StaleMonthsKey, Module: "assistant", Type: settings.Int, Default: "6", Check: settings.Between(1, 120),
	}))
}

func staleTool() tool.Tool {
	return tool.Tool{
		Name:        "stale_wiki",
		Description: "List wiki pages whose Last reviewed date is older than the configured threshold (nightly_stale_months). Re-check them during nightly and list them in the weekly review.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Handler:     stale,
		Effect:      tool.Read,
		Annotated:   true,
		Contexts:    tool.Kinds{tool.Nightly, tool.Cron},
	}
}

func stale(ctx context.Context, tc *tool.Context, _ tool.Args) (any, error) {
	months := int64(6)
	if found, err := settings.Default.Resolve(ctx, tc.DB.Read, StaleMonthsKey); err != nil {
		return nil, err
	} else if n, err := found.Int(); err == nil {
		months = n
	}
	cutoff := time.Now().AddDate(0, -int(months), 0).Format("2006-01-02")
	rows, err := tc.DB.Read.QueryContext(ctx,
		"SELECT page_id, title, last_reviewed FROM wiki_pages WHERE in_trash = 0 AND last_reviewed <> '' AND last_reviewed < ? ORDER BY last_reviewed, title", cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pages := []any{}
	for rows.Next() {
		var id, title, reviewed string
		if err := rows.Scan(&id, &title, &reviewed); err != nil {
			return nil, err
		}
		pages = append(pages, map[string]any{"page_id": id, "title": title, "last_reviewed": reviewed})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"months": months, "cutoff": cutoff, "pages": pages}, nil
}
