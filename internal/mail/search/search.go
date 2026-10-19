// Package search is search.py: hybrid mail search, FTS5 bm25 and vec0 KNN fused by reciprocal rank, with the
// lexical branch alone when the embeddings server is down.
package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	RRFK          = 60
	BranchLimit   = 50
	MaxTerms      = 12
	SnippetTokens = 24
	SnippetChars  = 220
	DefaultLimit  = 20
	start, end    = "\x02", "\x03"
)

var knnMax = embed.KNNMax

var (
	tokenRe = rx.MustCompile(`"([^"]*)"|([^\W_]+)`, 0)
	wordRe  = rx.MustCompile(`[^\W_]+`, 0)
	truthy  = map[string]bool{"1": true, "true": true, "on": true, "yes": true}
)

const baseSelect = "SELECT m.id, m.account, COALESCE(c.folder, m.folder, m.location) AS folder, m.from_addr, m.from_name, " +
	"m.subject, m.date_ts, m.seen, m.flagged, (m.att_types != '[]') AS has_attachment " +
	"FROM messages m LEFT JOIN corrections c ON c.message_id = m.id"

type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// FTSQuery is fts_query: every word a prefix term, every quoted phrase a phrase, at most MaxTerms terms.
func FTSQuery(text string) (string, error) {
	var terms []string
	matches, err := tokenRe.FindIter(text)
	if err != nil {
		return "", err
	}
	for _, m := range matches {
		if word, ok := m.GroupOK(2); ok && word != "" {
			terms = append(terms, `"`+word+`"*`)
			continue
		}
		phrase, _ := m.GroupOK(1)
		words, err := wordRe.FindAll(phrase)
		if err != nil {
			return "", err
		}
		if len(words) > 0 {
			terms = append(terms, `"`+strings.Join(words, " ")+`"`)
		}
	}
	return strings.Join(terms[:min(len(terms), MaxTerms)], " "), nil
}

// Scored is one rrf result.
type Scored struct {
	ID    int64
	Score float64
}

// RRF is rrf: reciprocal-rank fusion of the rankings, best score first, ties by best rank then id.
func RRF(rankings ...[]int64) []Scored {
	scores := map[int64]float64{}
	best := map[int64]int{}
	var order []int64
	for _, ranking := range rankings {
		for i, item := range ranking {
			rank := i + 1
			if _, seen := scores[item]; !seen {
				order = append(order, item)
				best[item] = rank
			}
			scores[item] = scores[item] + 1.0/float64(RRFK+rank)
			best[item] = min(best[item], rank)
		}
	}
	out := make([]Scored, len(order))
	for i, id := range order {
		out[i] = Scored{id, scores[id]}
	}
	slices.SortFunc(out, func(a, b Scored) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		if best[a.ID] != best[b.ID] {
			return best[a.ID] - best[b.ID]
		}
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return out
}

// Filters is normalize_filters' result, as search returns it.
type Filters struct {
	Account       *string  `json:"account"`
	Folder        *string  `json:"folder"`
	From          *string  `json:"from"`
	DateFrom      *float64 `json:"date_from"`
	DateTo        *float64 `json:"date_to"`
	HasAttachment bool     `json:"has_attachment"`
	Unread        bool     `json:"unread"`
	Flagged       bool     `json:"flagged"`
}

func flag(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return truthy[strings.ToLower(py.Strip(t))]
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case json.Number:
		f, _ := t.Float64()
		return f != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

func text(v any) (*string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		if s := py.Strip(t); s != "" {
			return &s, nil
		}
		return nil, nil
	}
	if !flag(v) {
		return nil, nil
	}
	return nil, fmt.Errorf("%w: '%s' object has no attribute 'strip'", httpx.ErrType, httpx.TypeName(v))
}

func timestamp(v any, endOfDay bool) (*float64, error) {
	var f float64
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		if t == "" {
			return nil, nil
		}
		day, ok := py.StrptimeDate(py.Strip(t))
		if !ok {
			return nil, fmt.Errorf("%w: time data %s does not match format '%%Y-%%m-%%d'", httpx.ErrValue, py.Repr(py.Strip(t)))
		}
		day.Aware = true
		f = day.Timestamp(time.UTC)
		if endOfDay {
			f += 86400
		}
		return &f, nil
	case float64:
		f = t
	case int:
		f = float64(t)
	case int64:
		f = float64(t)
	case bool:
		if t {
			f = 1
		}
	case json.Number:
		f, _ = t.Float64()
	default:
		return nil, fmt.Errorf("%w: unsupported date value %v", httpx.ErrValue, v)
	}
	return &f, nil
}

// NormalizeFilters is normalize_filters over the decoded JSON a caller supplied; a nil map filters nothing.
func NormalizeFilters(filters map[string]any) (Filters, error) {
	var f Filters
	var err error
	if f.Account, err = text(filters["account"]); err != nil {
		return f, err
	}
	if f.Folder, err = text(filters["folder"]); err != nil {
		return f, err
	}
	if f.From, err = text(filters["from"]); err != nil {
		return f, err
	}
	if f.DateFrom, err = timestamp(filters["date_from"], false); err != nil {
		return f, err
	}
	if f.DateTo, err = timestamp(filters["date_to"], true); err != nil {
		return f, err
	}
	f.HasAttachment, f.Unread, f.Flagged = flag(filters["has_attachment"]), flag(filters["unread"]), flag(filters["flagged"])
	return f, nil
}

// filterSQL is filter_sql with positional parameters in the order the named ones occur.
func filterSQL(f Filters) (string, []any) {
	clauses := []string{"m.gone_at IS NULL"}
	var params []any
	if f.Account != nil {
		clauses = append(clauses, "m.account = ?")
		params = append(params, *f.Account)
	}
	if f.Folder != nil {
		clauses = append(clauses, "(COALESCE(c.folder, m.folder) = ? OR instr('|' || m.location || '|', '|' || ? || '|') > 0)")
		params = append(params, *f.Folder, *f.Folder)
	}
	if f.From != nil {
		clauses = append(clauses, "(instr(lower(m.from_addr), lower(?)) > 0 OR instr(lower(m.from_name), lower(?)) > 0)")
		params = append(params, *f.From, *f.From)
	}
	if f.DateFrom != nil {
		clauses = append(clauses, "m.date_ts >= ?")
		params = append(params, *f.DateFrom)
	}
	if f.DateTo != nil {
		clauses = append(clauses, "m.date_ts < ?")
		params = append(params, *f.DateTo)
	}
	if f.HasAttachment {
		clauses = append(clauses, "m.att_types != '[]'")
	}
	if f.Unread {
		clauses = append(clauses, "m.seen = 0")
	}
	if f.Flagged {
		clauses = append(clauses, "m.flagged = 1")
	}
	return strings.Join(clauses, " AND "), params
}

// Part is one snippet segment and whether the match highlights it; it encodes as [text, highlighted].
type Part struct {
	Text        string
	Highlighted bool
}

func (p Part) MarshalJSON() ([]byte, error) { return json.Marshal([]any{p.Text, p.Highlighted}) }

// SnippetParts is snippet_parts: the text between the marker bytes snippet() put around matches.
func SnippetParts(marked string) []Part {
	parts := []Part{}
	for _, chunk := range strings.Split(marked, start) {
		if highlighted, plain, ok := strings.Cut(chunk, end); ok {
			parts = append(parts, Part{highlighted, true})
			if plain != "" {
				parts = append(parts, Part{plain, false})
			}
		} else if chunk != "" {
			parts = append(parts, Part{chunk, false})
		}
	}
	return parts
}

type lexicalHit struct {
	id      int64
	snippet string
}

func lexical(ctx context.Context, q Querier, match string, f Filters, limit int) ([]lexicalHit, error) {
	where, params := filterSQL(f)
	rows, err := q.QueryContext(ctx,
		fmt.Sprintf("SELECT m.id, snippet(message_fts, 2, '%s', '%s', '…', %d) AS snip ", start, end, SnippetTokens)+
			"FROM message_fts JOIN messages m ON m.id = message_fts.rowid "+
			"LEFT JOIN corrections c ON c.message_id = m.id WHERE message_fts MATCH ? AND "+where+
			" ORDER BY bm25(message_fts, 5.0, 2.0, 1.0) LIMIT ?",
		slices.Concat([]any{match}, params, []any{limit})...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (lexicalHit, error) {
		var h lexicalHit
		return h, r.Scan(&h.id, &h.snippet)
	})
}

func ids(rows *sql.Rows, err error) ([]int64, error) {
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
}

func jsonIDs(list []int64) string {
	raw, _ := json.Marshal(list)
	return string(raw)
}

func narrow(f Filters) bool {
	return f.Folder != nil || f.From != nil || f.HasAttachment || f.Unread || f.Flagged || f.DateFrom != nil || f.DateTo != nil
}

func semantic(ctx context.Context, q Querier, vector []float64, f Filters, limit int) ([]int64, error) {
	where, params := filterSQL(f)
	var total int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM message_vec").Scan(&total); err != nil {
		return nil, err
	}
	if total == 0 {
		return []int64{}, nil
	}
	blob := embed.Serialize(vector)
	k := limit * 4
	if narrow(f) {
		k = 400
	}
	for {
		k = min(k, total, knnMax)
		knn := "SELECT rowid FROM message_vec WHERE embedding MATCH ? AND k = ?"
		args := []any{blob, k}
		if f.Account != nil {
			knn += " AND account = ?"
			args = append(args, *f.Account)
		}
		hits, err := ids(q.QueryContext(ctx, knn, args...))
		if err != nil {
			return nil, err
		}
		permitted, err := infoRows(q.QueryContext(ctx, baseSelect+" WHERE m.id IN (SELECT value FROM json_each(?)) AND "+where,
			slices.Concat([]any{jsonIDs(hits)}, params)...))
		if err != nil {
			return nil, err
		}
		allowed := make(map[int64]bool, len(permitted))
		for _, info := range permitted {
			allowed[info.ID] = true
		}
		var ranked []int64
		for _, id := range hits {
			if allowed[id] {
				ranked = append(ranked, id)
			}
		}
		if len(ranked) >= limit || k >= total || len(hits) < k {
			return ranked[:min(len(ranked), limit)], nil
		}
		if k == knnMax {
			return exactSemantic(ctx, q, blob, where, params, limit)
		}
		k *= 4
	}
}

func exactSemantic(ctx context.Context, q Querier, blob []byte, where string, params []any, limit int) ([]int64, error) {
	return ids(q.QueryContext(ctx,
		"SELECT v.rowid FROM message_vec v WHERE v.rowid IN "+
			"(SELECT m.id FROM messages m LEFT JOIN corrections c ON c.message_id = m.id WHERE "+where+") "+
			"ORDER BY vec_distance_cosine(v.embedding, ?) LIMIT ?",
		slices.Concat(params, []any{blob, limit})...))
}

func bodyHead(ctx context.Context, q Querier, list []int64) (map[int64]string, error) {
	heads := map[int64]string{}
	if len(list) == 0 {
		return heads, nil
	}
	rows, err := q.QueryContext(ctx,
		"SELECT rowid, substr(body, 1, ?) AS head FROM message_fts WHERE rowid IN (SELECT value FROM json_each(?))",
		SnippetChars, jsonIDs(list))
	if err != nil {
		return nil, err
	}
	type head struct {
		id   int64
		text sql.NullString
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (head, error) {
		var h head
		return h, r.Scan(&h.id, &h.text)
	})
	for _, h := range found {
		heads[h.id] = strings.Join(py.Split(h.text.String), " ")
	}
	return heads, err
}

// Info is the message row a hit shows.
type Info struct {
	ID            int64   `json:"id"`
	Account       string  `json:"account"`
	Folder        string  `json:"folder"`
	FromAddr      string  `json:"from_addr"`
	FromName      string  `json:"from_name"`
	Subject       string  `json:"subject"`
	DateTS        float64 `json:"date_ts"`
	Seen          bool    `json:"seen"`
	Flagged       bool    `json:"flagged"`
	HasAttachment bool    `json:"has_attachment"`
}

func infoRows(rows *sql.Rows, err error) ([]Info, error) {
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Info, error) {
		var i Info
		var seen, flagged, att int64
		err := r.Scan(&i.ID, &i.Account, &i.Folder, &i.FromAddr, &i.FromName, &i.Subject, &i.DateTS, &seen, &flagged, &att)
		i.Seen, i.Flagged, i.HasAttachment = seen != 0, flagged != 0, att != 0
		return i, err
	})
}

func hydrate(ctx context.Context, q Querier, list []int64) (map[int64]Info, error) {
	out := map[int64]Info{}
	if len(list) == 0 {
		return out, nil
	}
	infos, err := infoRows(q.QueryContext(ctx, baseSelect+" WHERE m.id IN (SELECT value FROM json_each(?))", jsonIDs(list)))
	for _, i := range infos {
		out[i.ID] = i
	}
	return out, err
}

// Hit is one search result. Score and the ranks are null for the recent list and for a branch that missed it.
type Hit struct {
	Info
	Score        *float64 `json:"score"`
	LexicalRank  *int     `json:"lexical_rank"`
	SemanticRank *int     `json:"semantic_rank"`
	Snippet      string   `json:"snippet"`
	SnippetParts []Part   `json:"snippet_parts"`
}

func newHit(info Info, score *float64, lexRank, semRank *int, head string, marked *string) Hit {
	parts := []Part{}
	switch {
	case marked != nil && *marked != "":
		parts = SnippetParts(*marked)
	case head != "":
		parts = []Part{{head, false}}
	}
	var snippet strings.Builder
	for _, p := range parts {
		snippet.WriteString(p.Text)
	}
	return Hit{info, score, lexRank, semRank, snippet.String(), parts}
}

func recent(ctx context.Context, q Querier, f Filters, limit int) ([]Hit, error) {
	where, params := filterSQL(f)
	infos, err := infoRows(q.QueryContext(ctx, baseSelect+" WHERE "+where+" ORDER BY m.date_ts DESC, m.id DESC LIMIT ?",
		slices.Concat(params, []any{limit})...))
	if err != nil {
		return nil, err
	}
	list := make([]int64, len(infos))
	for i, info := range infos {
		list[i] = info.ID
	}
	heads, err := bodyHead(ctx, q, list)
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, len(infos))
	for i, info := range infos {
		hits[i] = newHit(info, nil, nil, nil, heads[info.ID], nil)
	}
	return hits, nil
}

// Result is what search returns; Error is the embeddings server's complaint when Semantic is false.
type Result struct {
	Hits     []Hit   `json:"hits"`
	Semantic bool    `json:"semantic"`
	Error    *string `json:"error"`
	Filters  Filters `json:"filters"`
}

func rank(list []int64) map[int64]int {
	out := make(map[int64]int, len(list))
	for i, id := range list {
		out[id] = i + 1
	}
	return out
}

// Search is search. A nil embedTexts is embed.Texts; limit is DefaultLimit unless the caller means otherwise.
func Search(ctx context.Context, q Querier, cfg config.Config, query string, filters map[string]any, limit int, embedTexts embed.TextsFunc) (Result, error) {
	if embedTexts == nil {
		embedTexts = embed.Texts
	}
	f, err := NormalizeFilters(filters)
	if err != nil {
		return Result{}, err
	}
	query = py.Strip(query)
	if query == "" {
		hits, err := recent(ctx, q, f, limit)
		return Result{Hits: hits, Filters: f}, err
	}
	match, err := FTSQuery(query)
	if err != nil {
		return Result{}, err
	}
	var lex []lexicalHit
	if match != "" {
		if lex, err = lexical(ctx, q, match, f, BranchLimit); err != nil {
			return Result{}, err
		}
	}
	var sem []int64
	var failure *string
	available := true
	vectors, err := embedTexts(ctx, cfg.EmbedURL, []string{embed.QueryText(query)})
	if err == nil {
		if len(vectors) == 0 {
			err = fmt.Errorf("%w: list index out of range", httpx.ErrIndex)
		} else {
			sem, err = semantic(ctx, q, vectors[0], f, BranchLimit)
		}
	}
	if err != nil {
		var unavailable *httpx.ServiceUnavailable
		if !errors.As(err, &unavailable) && !errors.Is(err, httpx.ErrValue) && !errors.Is(err, httpx.ErrKey) {
			return Result{}, err
		}
		available = false
		msg := httpx.Message(err)
		failure = &msg
		sem = nil
	}
	lexRank := map[int64]int{}
	marked := map[int64]string{}
	lexIDs := make([]int64, len(lex))
	for i, h := range lex {
		lexIDs[i] = h.id
		if _, dup := lexRank[h.id]; !dup {
			lexRank[h.id] = i + 1
		}
		marked[h.id] = h.snippet
	}
	semRank := rank(sem)
	fused := RRF(lexIDs, sem)
	fused = fused[:min(len(fused), max(limit, 0))]
	fusedIDs := make([]int64, len(fused))
	for i, s := range fused {
		fusedIDs[i] = s.ID
	}
	info, err := hydrate(ctx, q, fusedIDs)
	if err != nil {
		return Result{}, err
	}
	var unmarked []int64
	for _, id := range fusedIDs {
		if _, ok := marked[id]; !ok {
			unmarked = append(unmarked, id)
		}
	}
	heads, err := bodyHead(ctx, q, unmarked)
	if err != nil {
		return Result{}, err
	}
	hits := []Hit{}
	for _, s := range fused {
		i, ok := info[s.ID]
		if !ok {
			continue
		}
		var lr, sr *int
		if n, ok := lexRank[s.ID]; ok {
			lr = &n
		}
		if n, ok := semRank[s.ID]; ok {
			sr = &n
		}
		var m *string
		if text, ok := marked[s.ID]; ok {
			m = &text
		}
		score := s.Score
		hits = append(hits, newHit(i, &score, lr, sr, heads[s.ID], m))
	}
	return Result{Hits: hits, Semantic: available, Error: failure, Filters: f}, nil
}
