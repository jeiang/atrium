package notion

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/search"
)

const goldenPageID = "00000000-0000-0000-0000-000000000001"

type lexicalRow struct {
	PageRef
	LexicalRank  *int          `json:"lexical_rank"`
	Snippet      string        `json:"snippet"`
	SnippetParts []search.Part `json:"snippet_parts"`
}

func lexicalRows(hits []Hit) []lexicalRow {
	var ranked []Hit
	for _, h := range hits {
		if h.LexicalRank != nil {
			ranked = append(ranked, h)
		}
	}
	slices.SortFunc(ranked, func(a, b Hit) int { return cmp.Compare(*a.LexicalRank, *b.LexicalRank) })
	out := make([]lexicalRow, len(ranked))
	for i, h := range ranked {
		out[i] = lexicalRow{h.PageRef, h.LexicalRank, h.Snippet, h.SnippetParts}
	}
	return out
}

func noEmbedder(context.Context, string, []string) ([][]float64, error) {
	return nil, &httpx.ServiceUnavailable{Msg: "no embedder"}
}

var _ embed.TextsFunc = noEmbedder

func init() {
	for _, c := range []struct {
		golden  string
		private bool
	}{{"notion_search.search alpha", false}, {"notion_search.search alpha private", true}} {
		readpaths.Register(readpaths.Path{
			Name:   "notion.search lexical " + c.golden,
			Golden: c.golden,
			Run: func(ctx context.Context, d *db.DB) (any, error) {
				result, err := Search(ctx, d.Read, config.Config{}, "alpha", c.private, 20, noEmbedder)
				return lexicalRows(result.Hits), err
			},
			Want: func(recorded json.RawMessage) (any, error) {
				var result struct {
					Hits []struct {
						PageRef
						LexicalRank  *int            `json:"lexical_rank"`
						Snippet      string          `json:"snippet"`
						SnippetParts json.RawMessage `json:"snippet_parts"`
					} `json:"hits"`
				}
				if err := json.Unmarshal(recorded, &result); err != nil {
					return nil, err
				}
				type row struct {
					PageRef
					LexicalRank  *int            `json:"lexical_rank"`
					Snippet      string          `json:"snippet"`
					SnippetParts json.RawMessage `json:"snippet_parts"`
				}
				var ranked []row
				for _, h := range result.Hits {
					if h.LexicalRank != nil {
						ranked = append(ranked, row(h))
					}
				}
				slices.SortFunc(ranked, func(a, b row) int { return cmp.Compare(*a.LexicalRank, *b.LexicalRank) })
				return ranked, nil
			},
		})
	}
	readpaths.Register(readpaths.Path{
		Name:   "notion.get_page",
		Golden: "notion_search.get_page",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return GetPage(ctx, d.Read, goldenPageID)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "notion.related",
		Golden: "notion_search.related",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return RelatedPages(ctx, d.Read, goldenPageID, RelatedLimit)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "notion.counts",
		Golden: "notion_sync.counts",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return Totals(ctx, d.Read)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "notion.GetWiki",
		Golden: "notion_wiki.get_local",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return GetWiki(ctx, d.Read, goldenPageID)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "notion.LogPageID",
		Golden: "notion_wiki.log_page_id",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			var one int
			if err := d.Read.QueryRowContext(ctx, "SELECT 1 FROM notion_state WHERE key = ?", logStateKey).Scan(&one); err != nil {
				return nil, err
			}
			return LogPageID(ctx, d, nil, "")
		},
	})
}
