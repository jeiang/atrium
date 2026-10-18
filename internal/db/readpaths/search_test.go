package readpaths_test

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

const alphaPageID = "00000000-0000-0000-0000-000000000001"

type hit struct {
	ID           json.RawMessage `json:"id"`
	LexicalRank  *int            `json:"lexical_rank"`
	SemanticRank *int            `json:"semantic_rank"`
	Linked       bool            `json:"linked"`
	Score        float64         `json:"score"`
}

type lexRow struct {
	ID json.RawMessage `json:"id"`
}

type scored struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
}

func hits(recorded json.RawMessage) ([]hit, error) {
	var withHits struct {
		Hits []hit `json:"hits"`
	}
	if err := json.Unmarshal(recorded, &withHits); err == nil && withHits.Hits != nil {
		return withHits.Hits, nil
	}
	var list []hit
	return list, json.Unmarshal(recorded, &list)
}

func byRank(recorded json.RawMessage, rank func(hit) *int, row func(hit) any) (any, error) {
	all, err := hits(recorded)
	if err != nil {
		return nil, err
	}
	var ranked []hit
	for _, h := range all {
		if rank(h) != nil {
			ranked = append(ranked, h)
		}
	}
	slices.SortFunc(ranked, func(a, b hit) int { return cmp.Compare(*rank(a), *rank(b)) })
	out := make([]any, len(ranked))
	for i, h := range ranked {
		out[i] = row(h)
	}
	return out, nil
}

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "db.notion_fts lexical",
		Golden: "notion_search.search alpha private",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			rows, err := d.Read.QueryContext(ctx,
				"SELECT p.id FROM notion_fts JOIN notion_pages p ON p.rid = notion_fts.rowid "+
					"WHERE notion_fts MATCH ? AND p.in_trash = 0 AND (? = 1 OR p.private = 0) "+
					"ORDER BY bm25(notion_fts, 5.0, 2.0, 1.0) LIMIT 50", `"alpha"*`, 1)
			if err != nil {
				return nil, err
			}
			return db.Collect(rows, func(r *sql.Rows) (lexRow, error) {
				var id string
				err := r.Scan(&id)
				raw, _ := json.Marshal(id)
				return lexRow{ID: raw}, err
			})
		},
		Want: func(recorded json.RawMessage) (any, error) {
			return byRank(recorded, func(h hit) *int { return h.LexicalRank }, func(h hit) any { return lexRow{ID: h.ID} })
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "db.notion_vec knn",
		Golden: "notion_search.related",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			var rid int64
			var private bool
			if err := d.Read.QueryRowContext(ctx, "SELECT rid, private FROM notion_pages WHERE id = ?", alphaPageID).Scan(&rid, &private); err != nil {
				return nil, err
			}
			visibilities := []string{"public"}
			if private {
				visibilities = append(visibilities, "private")
			}
			type near struct {
				id       string
				distance float64
			}
			var all []near
			for _, vis := range visibilities {
				rows, err := d.Read.QueryContext(ctx,
					"SELECT p.id, v.distance FROM notion_vec v JOIN notion_pages p ON p.rid = v.rowid "+
						"WHERE v.embedding MATCH (SELECT embedding FROM notion_vec WHERE rowid = ?) "+
						"AND v.k = (SELECT MIN(12, COUNT(*)) FROM notion_vec) AND v.visibility = ? "+
						"AND p.in_trash = 0 AND p.rid != ? ORDER BY v.distance", rid, vis, rid)
				if err != nil {
					return nil, err
				}
				got, err := db.Collect(rows, func(r *sql.Rows) (near, error) {
					var n near
					return n, r.Scan(&n.id, &n.distance)
				})
				if err != nil {
					return nil, err
				}
				all = append(all, got...)
			}
			slices.SortStableFunc(all, func(a, b near) int { return cmp.Compare(a.distance, b.distance) })
			out := make([]scored, 0, len(all))
			for _, n := range all {
				out = append(out, scored{ID: n.id, Score: max(0, 1-n.distance)})
			}
			return out[:min(len(out), 10)], nil
		},
		Want: func(recorded json.RawMessage) (any, error) {
			all, err := hits(recorded)
			if err != nil {
				return nil, err
			}
			var out []scored
			for _, h := range all {
				if !h.Linked {
					var id string
					if err := json.Unmarshal(h.ID, &id); err != nil {
						return nil, err
					}
					out = append(out, scored{ID: id, Score: h.Score})
				}
			}
			return out, nil
		},
	})
}
