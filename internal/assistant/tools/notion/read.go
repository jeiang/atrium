package notiontools

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/notionmd"
	"github.com/jeiang/atrium/internal/tool"
)

var readKinds = tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub}

func clip(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

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

func withMeta(ctx context.Context, tc *tool.Context, d map[string]any, pageID string) (map[string]any, error) {
	aliases, reviewed, err := meta(ctx, tc, pageID)
	if err != nil {
		return nil, err
	}
	d["aliases"] = aliases
	d["last_reviewed"] = reviewed
	return d, nil
}

func aliasHits(ctx context.Context, tc *tool.Context, query string, snippetChars int) ([]map[string]any, error) {
	ids, err := notion.AliasMatches(ctx, tc.DB.Read, query)
	if err != nil {
		return nil, err
	}
	limits, err := notion.LoadSearch(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	hits := []map[string]any{}
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
		hit := map[string]any{"page_id": id}
		if row != nil {
			head := clip(row.Markdown, limits.HeadSource)
			hit["title"] = row.Title
			hit["path"] = row.Path
			hit["url"] = row.URL
			hit["snippet"] = tool.Clip(notionmd.Plain(head, map[string]string{}), snippetChars)
		} else {
			title := ""
			if local != nil {
				title = local.Title
			}
			hit["title"] = title
			hit["path"] = ""
			hit["url"] = ""
			hit["snippet"] = ""
		}
		hit["match"] = "alias"
		if hit, err = withMeta(ctx, tc, hit, id); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

func searchKnowledge(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	query, err := required(args, "query")
	if err != nil {
		return nil, err
	}
	r := assistant.Read(ctx, tc.DB.Read)
	snippetChars := r.Int(assistant.KeyNotionSnippet)
	if err := r.Err(); err != nil {
		return nil, err
	}
	limit, err := notionLimitBound.Clamp(ctx, tc.DB.Read, args, "limit")
	if err != nil {
		return nil, err
	}
	results, err := aliasHits(ctx, tc, query, snippetChars)
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
	found, err := notion.Search(ctx, tc.DB.KNN, query, false, int(limit), embedTexts)
	if err != nil {
		return nil, err
	}
	for _, h := range found.Hits {
		if seen[h.ID] {
			continue
		}
		hit, err := withMeta(ctx, tc, map[string]any{"page_id": h.ID, "title": h.Title, "path": h.Folder, "url": h.URL, "snippet": tool.Clip(h.Snippet, snippetChars), "match": "search"}, h.ID)
		if err != nil {
			return nil, err
		}
		results = append(results, hit)
	}
	out := make([]any, 0, limit)
	for i := 0; i < len(results) && int64(i) < limit; i++ {
		out = append(out, results[i])
	}
	return map[string]any{"semantic": found.Semantic, "results": out}, nil
}

func livePage(ctx context.Context, tc *tool.Context, pageID string) (map[string]any, error) {
	client, err := makeClient(ctx, tc.DB.Read, tc.Env)
	if err != nil {
		return nil, err
	}
	page, err := client.GetPage(ctx, pageID)
	if err != nil {
		return nil, notionError(err)
	}
	sensitivity, err := notion.LoadSensitivity(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	if sensitivity.IsPrivate(sensitivity.Of(page.Properties)) {
		return nil, tool.Error("page is private")
	}
	markdown, err := notion.FetchMarkdown(ctx, client, pageID)
	if err != nil {
		return nil, notionError(err)
	}
	return map[string]any{"page_id": normalize(pageID), "title": page.Title(), "url": page.URL, "markdown": markdown}, nil
}

func fetchKnowledge(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw, err := required(args, "page_id")
	if err != nil {
		return nil, err
	}
	pageID := normalize(raw)
	r := assistant.Read(ctx, tc.DB.Read)
	staleSecs := r.Int(assistant.KeyNotionStaleSecs)
	if err := r.Err(); err != nil {
		return nil, err
	}
	maxChars, err := notionCharsBound.Clamp(ctx, tc.DB.Read, args, "max_chars")
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
	var page map[string]any
	source := "live"
	if row != nil && row.InTrash == 0 && tc.Now()-row.SyncedAt < float64(staleSecs) && row.Markdown != "" {
		page = map[string]any{"page_id": row.ID, "title": row.Title, "url": row.URL, "markdown": row.Markdown}
		source = "mirror"
	} else if page, err = livePage(ctx, tc, pageID); err != nil {
		return nil, err
	}
	text := str(page, "markdown")
	page["markdown"] = clip(text, int(maxChars))
	page["truncated"] = int64(len([]rune(text))) > maxChars
	page["source"] = source
	return withMeta(ctx, tc, page, pageID)
}

var (
	notionLimitBound = tool.NewBound(assistant.KeyNotionMaxResults, "")
	notionCharsBound = tool.NewBound(assistant.KeyNotionPageChars, "")
)

func readTools() []tool.Tool {
	return []tool.Tool{
		{
			Name:        "search_knowledge",
			Description: "Search the operator's Notion wiki mirror: exact title/alias matches first, then full-text plus semantic hits. Private pages are never returned. Use fetch_knowledge for a full page; search two or three name variants before creating a page.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"query": {"type": "string"}, "limit": ` + notionLimitBound.Schema() + `}, "required": ["query"], "additionalProperties": false}`),
			Handler:     searchKnowledge,
			Effect:      tool.Read,
			Annotated:   true,
			Marks:       tool.Personal,
			Contexts:    readKinds,
		},
		{
			Name:        "fetch_knowledge",
			Description: "Fetch a Notion page's markdown by page_id from the local mirror, falling back to live Notion when the mirror is missing or stale. Check its Last reviewed date and dated 'as of' claims; report the page as possibly stale when they are old.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"page_id": {"type": "string"}, "max_chars": ` + notionCharsBound.Schema() + `}, "required": ["page_id"], "additionalProperties": false}`),
			Handler:     fetchKnowledge,
			Effect:      tool.Read,
			Annotated:   true,
			Marks:       tool.Personal,
			Contexts:    readKinds,
		},
	}
}
