package notiontools

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/py"
)

const (
	maxResults   = 10
	pageDefault  = 12000
	staleSeconds = 24 * 3600
	snippetChars = 300
)

var readKinds = tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub}

func meta(ctx context.Context, tc *tool.Context, pageID string) ([]any, string, error) {
	local, err := notion.GetWiki(ctx, tc.DB.Read, pageID)
	if err != nil || local == nil {
		return []any{}, "", err
	}
	aliases := make([]any, len(local.Aliases))
	for i, a := range local.Aliases {
		aliases[i] = a
	}
	return aliases, local.LastReviewed, nil
}

func withMeta(ctx context.Context, tc *tool.Context, d py.Dict, pageID string) (py.Dict, error) {
	aliases, reviewed, err := meta(ctx, tc, pageID)
	if err != nil {
		return nil, err
	}
	return append(d, py.Pair{Key: "aliases", Value: aliases}, py.Pair{Key: "last_reviewed", Value: reviewed}), nil
}

func aliasHits(ctx context.Context, tc *tool.Context, query string) ([]py.Dict, error) {
	ids, err := notion.AliasMatches(ctx, tc.DB.Read, query)
	if err != nil {
		return nil, err
	}
	hits := []py.Dict{}
	for _, id := range ids {
		row, err := notion.GetPage(ctx, tc.DB.Read, id)
		if err != nil {
			return nil, err
		}
		if row != nil && (row.InTrash != 0 || row.Private != 0) {
			continue
		}
		local, err := notion.GetWiki(ctx, tc.DB.Read, id)
		if err != nil {
			return nil, err
		}
		hit := py.Dict{{Key: "page_id", Value: id}}
		if row != nil {
			head := py.Clip(row.Markdown, notion.HeadSourceChars)
			hit = append(hit, py.Pair{Key: "title", Value: row.Title}, py.Pair{Key: "path", Value: row.Path}, py.Pair{Key: "url", Value: row.URL},
				py.Pair{Key: "snippet", Value: tool.Clip(notion.Plain(head, map[string]string{}), snippetChars)})
		} else {
			title := ""
			if local != nil {
				title = local.Title
			}
			hit = append(hit, py.Pair{Key: "title", Value: title}, py.Pair{Key: "path", Value: ""}, py.Pair{Key: "url", Value: ""}, py.Pair{Key: "snippet", Value: ""})
		}
		hit = append(hit, py.Pair{Key: "match", Value: "alias"})
		if hit, err = withMeta(ctx, tc, hit, id); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

func searchKnowledge(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	query, err := required(args, "query")
	if err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", 5, maxResults, 1)
	if err != nil {
		return nil, err
	}
	results, err := aliasHits(ctx, tc, query)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, r := range results {
		seen[str(r, "page_id")] = true
	}
	embedTexts := func(ctx context.Context, _ string, texts []string) ([][]float64, error) {
		if tc.Embed == nil {
			return nil, &httpx.ServiceUnavailable{Msg: "no embedder configured"}
		}
		return tc.Embed(ctx, texts)
	}
	found, err := notion.Search(ctx, tc.DB.Read, tc.Cfg, query, false, int(limit), embedTexts)
	if err != nil {
		return nil, err
	}
	for _, h := range found.Hits {
		if seen[h.ID] {
			continue
		}
		hit, err := withMeta(ctx, tc, py.Dict{
			{Key: "page_id", Value: h.ID}, {Key: "title", Value: h.Title}, {Key: "path", Value: h.Folder}, {Key: "url", Value: h.URL},
			{Key: "snippet", Value: tool.Clip(h.Snippet, snippetChars)}, {Key: "match", Value: "search"},
		}, h.ID)
		if err != nil {
			return nil, err
		}
		results = append(results, hit)
	}
	out := make([]any, 0, limit)
	for i := 0; i < len(results) && int64(i) < limit; i++ {
		out = append(out, results[i])
	}
	return py.Dict{{Key: "semantic", Value: found.Semantic}, {Key: "results", Value: out}}, nil
}

func livePage(ctx context.Context, tc *tool.Context, pageID string) (py.Dict, error) {
	client, err := makeClient(tc.Env)
	if err != nil {
		return nil, err
	}
	page, err := client.GetPage(ctx, pageID)
	if err != nil {
		return nil, notionError(err)
	}
	properties, _ := dictGet(page, "properties").(py.Dict)
	if notion.IsPrivateValue(notion.SensitivityOf(properties)) {
		return nil, tool.Error("page is private")
	}
	markdown, err := notion.FetchMarkdown(ctx, client, pageID)
	if err != nil {
		return nil, notionError(err)
	}
	return py.Dict{
		{Key: "page_id", Value: normalize(pageID)}, {Key: "title", Value: notion.PageTitle(page)},
		{Key: "url", Value: str(page, "url")}, {Key: "markdown", Value: markdown},
	}, nil
}

func fetchKnowledge(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	raw, err := required(args, "page_id")
	if err != nil {
		return nil, err
	}
	pageID := normalize(raw)
	maxChars, err := tool.BoundedInt(args, "max_chars", pageDefault, pageDefault, 1)
	if err != nil {
		return nil, err
	}
	row, err := notion.GetPage(ctx, tc.DB.Read, pageID)
	if err != nil {
		return nil, err
	}
	if row != nil && row.Private != 0 {
		return nil, tool.Error("page is private")
	}
	var page py.Dict
	source := "live"
	if row != nil && row.InTrash == 0 && tc.Now()-row.SyncedAt < staleSeconds && row.Markdown != "" {
		page = py.Dict{{Key: "page_id", Value: row.ID}, {Key: "title", Value: row.Title}, {Key: "url", Value: row.URL}, {Key: "markdown", Value: row.Markdown}}
		source = "mirror"
	} else if page, err = livePage(ctx, tc, pageID); err != nil {
		return nil, err
	}
	text := str(page, "markdown")
	page = page.Set("markdown", py.Clip(text, int(maxChars)))
	page = append(page, py.Pair{Key: "truncated", Value: int64(len([]rune(text))) > maxChars}, py.Pair{Key: "source", Value: source})
	return withMeta(ctx, tc, page, pageID)
}

func readTools() []tool.Tool {
	return []tool.Tool{
		{
			Name:        "search_knowledge",
			Description: "Search the operator's Notion wiki mirror: exact title/alias matches first, then full-text plus semantic hits. Private pages are never returned. Use fetch_knowledge for a full page.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"query": {"type": "string"}, "limit": {"type": "integer", "minimum": 1, "maximum": 10}}, "required": ["query"], "additionalProperties": false}`),
			Handler:     searchKnowledge,
			Effect:      tool.Read,
			Contexts:    readKinds,
		},
		{
			Name:        "fetch_knowledge",
			Description: "Fetch a Notion page's markdown by page_id from the local mirror, falling back to live Notion when the mirror is missing or stale.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"page_id": {"type": "string"}, "max_chars": {"type": "integer", "minimum": 1, "maximum": 12000}}, "required": ["page_id"], "additionalProperties": false}`),
			Handler:     fetchKnowledge,
			Effect:      tool.Read,
			Contexts:    readKinds,
		},
	}
}
