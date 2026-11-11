package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/textsearch"
)

const (
	lexicalLimit  = 50
	semanticLimit = 50
	exactMinChars = 3
)

type SearchFilter struct {
	Query     string
	Scope     string
	Limit     int
	ProjectID int64
	Now       float64
	Embed     func(ctx context.Context, texts []string) ([][]float64, error)
}

type Hit struct {
	Record
	Cite    string `json:"cite"`
	Snippet string `json:"snippet,omitempty"`
}

type Result struct {
	Hits     []Hit `json:"results"`
	Semantic bool  `json:"semantic"`
}

type exactRow struct {
	id      int64
	title   string
	aliases string
}

func exactMatches(ctx context.Context, q db.Querier, f SearchFilter) ([]int64, error) {
	where, liveArgs := liveWhere("r", f.ProjectID, f.Now, f.Scope)
	rows, err := q.QueryContext(ctx, "SELECT r.id, r.title, r.aliases"+fromClause()+" WHERE "+where, liveArgs...)
	if err != nil {
		return nil, err
	}
	list, err := db.Collect(rows, func(r *sql.Rows) (exactRow, error) {
		var e exactRow
		err := r.Scan(&e.id, &e.title, &e.aliases)
		return e, err
	})
	if err != nil {
		return nil, err
	}
	query := normalize(f.Query)
	type match struct {
		id   int64
		size int
	}
	var found []match
	for _, e := range list {
		var aliases []string
		_ = json.Unmarshal([]byte(e.aliases), &aliases)
		best := 0
		for _, candidate := range append([]string{e.title}, aliases...) {
			norm := normalize(candidate)
			if len(norm) < exactMinChars || !strings.Contains(query, norm) {
				continue
			}
			best = max(best, len(norm))
		}
		if best > 0 {
			found = append(found, match{e.id, best})
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].size != found[j].size {
			return found[i].size > found[j].size
		}
		return found[i].id < found[j].id
	})
	out := make([]int64, len(found))
	for i, m := range found {
		out[i] = m.id
	}
	return out, nil
}

type lexicalHit struct {
	id      int64
	snippet string
}

func lexical(ctx context.Context, q db.Querier, match string, f SearchFilter) ([]lexicalHit, error) {
	where, args := liveWhere("r", f.ProjectID, f.Now, f.Scope)
	args = append([]any{match}, args...)
	args = append(args, lexicalLimit)
	rows, err := q.QueryContext(ctx,
		fmt.Sprintf("SELECT r.id, snippet(kb_fts, -1, '%s', '%s', '…', %d) AS snip", textsearch.MarkStart, textsearch.MarkEnd, textsearch.SnippetTokens)+
			fromClause()+" JOIN kb_fts ON kb_fts.rowid = r.id WHERE kb_fts MATCH ? AND "+where+
			" ORDER BY bm25(kb_fts, 5.0, 3.0, 1.0) LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (lexicalHit, error) {
		var h lexicalHit
		err := r.Scan(&h.id, &h.snippet)
		return h, err
	})
}

func knn(ctx context.Context, q db.Querier, idx embed.Index, blob []byte, k int) ([]int64, error) {
	total, err := embed.KBCount(ctx, q, idx)
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, nil
	}
	k = min(k, total, embed.KNNMax)
	return embed.KBIDs(ctx, q, idx, blob, k)
}

func visibleIDs(ctx context.Context, q db.Querier, ids []int64, f SearchFilter) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	where, args := liveWhere("r", f.ProjectID, f.Now, f.Scope)
	args = append(args, string(raw))
	rows, err := q.QueryContext(ctx, "SELECT r.id"+fromClause()+" WHERE "+where+" AND r.id IN (SELECT value FROM json_each(?))", args...)
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		err := r.Scan(&id)
		return id, err
	})
	for _, id := range found {
		out[id] = true
	}
	return out, err
}

func hydrate(ctx context.Context, q db.Querier, ids []int64, f SearchFilter) (map[int64]Record, error) {
	out := map[int64]Record{}
	if len(ids) == 0 {
		return out, nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	where, args := liveWhere("r", f.ProjectID, f.Now, f.Scope)
	args = append(args, string(raw))
	rows, err := q.QueryContext(ctx, "SELECT "+columns+fromClause()+" WHERE "+where+" AND r.id IN (SELECT value FROM json_each(?))", args...)
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (Record, error) { return scan(r) })
	for _, r := range found {
		out[r.ID] = r
	}
	return out, err
}

func Search(ctx context.Context, q db.Querier, f SearchFilter) (Result, error) {
	if f.Limit <= 0 {
		f.Limit = 10
	}
	query := strings.TrimSpace(f.Query)
	if query == "" {
		return Result{Hits: []Hit{}}, nil
	}
	exact, err := exactMatches(ctx, q, f)
	if err != nil {
		return Result{}, err
	}
	match, err := textsearch.FTSQuery(query)
	if err != nil {
		return Result{}, err
	}
	var lex []lexicalHit
	if match != "" {
		if lex, err = lexical(ctx, q, match, f); err != nil {
			return Result{}, err
		}
	}
	lexIDs := make([]int64, len(lex))
	snippets := map[int64]string{}
	for i, h := range lex {
		lexIDs[i] = h.id
		snippets[h.id] = h.snippet
	}
	var sem []int64
	semantic := false
	if f.Embed != nil {
		idx, ierr := embed.Active(ctx, q)
		if ierr == nil {
			vectors, verr := f.Embed(ctx, []string{embed.QueryText(query)})
			if verr == nil && len(vectors) > 0 {
				near, kerr := knn(ctx, q, idx, embed.Serialize(vectors[0]), semanticLimit)
				if kerr != nil {
					return Result{}, kerr
				}
				visible, verr2 := visibleIDs(ctx, q, near, f)
				if verr2 != nil {
					return Result{}, verr2
				}
				for _, id := range near {
					if visible[id] {
						sem = append(sem, id)
					}
				}
				semantic = true
			} else if verr != nil {
				var unavailable *httpx.ServiceUnavailable
				if !errors.As(verr, &unavailable) && !errors.Is(verr, httpx.ErrPayload) && !errors.Is(verr, httpx.ErrMissing) {
					return Result{}, verr
				}
			}
		}
	}
	taken := map[int64]bool{}
	var order []int64
	for _, id := range exact {
		if taken[id] {
			continue
		}
		taken[id] = true
		order = append(order, id)
	}
	for _, s := range textsearch.RRF(lexIDs, sem) {
		if taken[s.ID] {
			continue
		}
		taken[s.ID] = true
		order = append(order, s.ID)
	}
	order = order[:min(len(order), f.Limit)]
	records, err := hydrate(ctx, q, order, f)
	if err != nil {
		return Result{}, err
	}
	hits := make([]Hit, 0, len(order))
	for _, id := range order {
		r, ok := records[id]
		if !ok {
			continue
		}
		snippet := snippets[id]
		if snippet == "" {
			snippet = r.Body
		}
		hits = append(hits, Hit{Record: r, Cite: Cite(r.ID), Snippet: snippet})
	}
	return Result{Hits: hits, Semantic: semantic}, nil
}
