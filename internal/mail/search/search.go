package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/textsearch"
	"github.com/jeiang/atrium/internal/timex"
)

type limits struct {
	rrfK, branchLimit, maxTerms, snippetTokens, snippetChars, knnMax int
}

func readLimits(ctx context.Context, q Querier) (limits, error) {
	l := limits{
		rrfK: mail.TuningMailSearchRrfK, branchLimit: mail.TuningMailSearchBranchLimit, maxTerms: mail.TuningMailSearchMaxTerms,
		snippetTokens: mail.TuningMailSearchSnippetTokens, snippetChars: mail.TuningMailSearchSnippetChars, knnMax: mail.TuningKnnMax,
	}
	return l, nil
}

const baseSelect = "SELECT m.id, m.account, COALESCE(c.folder, m.folder, m.location) AS folder, m.from_addr, m.from_name, " +
	"m.subject, m.date_ts FROM messages m LEFT JOIN corrections c ON c.message_id = m.id"

type Querier = db.Querier

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

func Text(value string) *string {
	if value = strings.TrimSpace(value); value == "" {
		return nil
	}
	return &value
}

func Day(value string, endOfDay bool) (*float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	day, ok := timex.ParseDay(value)
	if !ok {
		return nil, fmt.Errorf("%w: invalid date %q, want YYYY-MM-DD", httpx.ErrPayload, value)
	}
	ts := float64(day.Unix())
	if endOfDay {
		ts += 86400
	}
	return &ts, nil
}

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

type lexicalHit struct {
	id      int64
	snippet string
}

func lexical(ctx context.Context, q Querier, match string, f Filters, limit, snippetTokens int) ([]lexicalHit, error) {
	where, params := filterSQL(f)
	rows, err := q.QueryContext(ctx,
		fmt.Sprintf("SELECT m.id, snippet(message_fts, 2, '%s', '%s', '…', %d) AS snip ", textsearch.MarkStart, textsearch.MarkEnd, snippetTokens)+
			"FROM message_fts JOIN messages m ON m.id = message_fts.rowid "+
			"LEFT JOIN corrections c ON c.message_id = m.id WHERE message_fts MATCH ? AND "+where+
			" ORDER BY bm25(message_fts, 5.0, 2.0, 1.0) LIMIT ?",
		slices.Concat([]any{match}, params, []any{limit})...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (lexicalHit, error) {
		var h lexicalHit
		err := r.Scan(&h.id, &h.snippet)
		return h, err
	})
}

func ids(rows *sql.Rows, err error) ([]int64, error) {
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		err := r.Scan(&id)
		return id, err
	})
}

func jsonIDs(list []int64) string {
	raw, _ := json.Marshal(list)
	return string(raw)
}

func narrow(f Filters) bool {
	return f.Folder != nil || f.From != nil || f.HasAttachment || f.Unread || f.Flagged || f.DateFrom != nil || f.DateTo != nil
}

func semantic(ctx context.Context, q Querier, vector []float64, f Filters, limit, knnMax int) ([]int64, error) {
	where, params := filterSQL(f)
	idx, err := embed.Active(ctx, q)
	if err != nil {
		return nil, err
	}
	total, err := embed.MessageCount(ctx, q, idx)
	if err != nil {
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
		hits, err := embed.MessageIDs(ctx, q, idx, blob, k, f.Account)
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
			return exactSemantic(ctx, q, idx, blob, where, params, limit)
		}
		k *= 4
	}
}

func exactSemantic(ctx context.Context, q Querier, idx embed.Index, blob []byte, where string, params []any, limit int) ([]int64, error) {
	candidates, err := ids(q.QueryContext(ctx,
		"SELECT m.id FROM messages m LEFT JOIN corrections c ON c.message_id = m.id WHERE "+where, params...))
	if err != nil {
		return nil, err
	}
	return embed.MessageRank(ctx, q, idx, blob, candidates, limit)
}

func bodyHead(ctx context.Context, q Querier, list []int64, snippetChars int) (map[int64]string, error) {
	heads := map[int64]string{}
	if len(list) == 0 {
		return heads, nil
	}
	rows, err := q.QueryContext(ctx,
		"SELECT rowid, substr(body, 1, ?) AS head FROM message_fts WHERE rowid IN (SELECT value FROM json_each(?))",
		snippetChars, jsonIDs(list))
	if err != nil {
		return nil, err
	}
	type head struct {
		id   int64
		text sql.NullString
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (head, error) {
		var h head
		err := r.Scan(&h.id, &h.text)
		return h, err
	})
	for _, h := range found {
		heads[h.id] = strings.Join(strings.Fields(h.text.String), " ")
	}
	return heads, err
}

type Info struct {
	ID       int64   `json:"id"`
	Account  string  `json:"account"`
	Folder   string  `json:"folder"`
	FromAddr string  `json:"from_addr"`
	FromName string  `json:"from_name"`
	Subject  string  `json:"subject"`
	DateTS   float64 `json:"date_ts"`
}

func infoRows(rows *sql.Rows, err error) ([]Info, error) {
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Info, error) {
		var i Info
		err := r.Scan(&i.ID, &i.Account, &i.Folder, &i.FromAddr, &i.FromName, &i.Subject, &i.DateTS)
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

type Hit struct {
	Info
	Snippet string `json:"snippet"`
}

func newHit(info Info, head string, marked *string) Hit {
	parts := []textsearch.Part{}
	switch {
	case marked != nil && *marked != "":
		parts = textsearch.SnippetParts(*marked)
	case head != "":
		parts = []textsearch.Part{{Text: head}}
	}
	var snippet strings.Builder
	for _, p := range parts {
		snippet.WriteString(p.Text)
	}
	return Hit{Info: info, Snippet: snippet.String()}
}

func recent(ctx context.Context, q Querier, f Filters, limit, snippetChars int) ([]Hit, error) {
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
	heads, err := bodyHead(ctx, q, list, snippetChars)
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, len(infos))
	for i, info := range infos {
		hits[i] = newHit(info, heads[info.ID], nil)
	}
	return hits, nil
}

type Result struct {
	Hits     []Hit `json:"hits"`
	Semantic bool  `json:"semantic"`
}

type Query struct {
	Text    string
	Filters Filters
	Limit   int
}

func Search(ctx context.Context, q Querier, req Query, embedTexts embed.TextsFunc) (Result, error) {
	if embedTexts == nil {
		embedTexts = embed.Texts
	}
	f, limit := req.Filters, req.Limit
	lim, err := readLimits(ctx, q)
	if err != nil {
		return Result{}, err
	}
	embedURL, err := embed.EmbedURL(ctx, q)
	if err != nil {
		return Result{}, err
	}
	ctx, err = embed.WithModels(ctx, q)
	if err != nil {
		return Result{}, err
	}
	query := strings.TrimSpace(req.Text)
	if query == "" {
		hits, err := recent(ctx, q, f, limit, lim.snippetChars)
		return Result{Hits: hits}, err
	}
	match, err := textsearch.FTSQueryTerms(query, lim.maxTerms)
	if err != nil {
		return Result{}, err
	}
	var lex []lexicalHit
	if match != "" {
		if lex, err = lexical(ctx, q, match, f, lim.branchLimit, lim.snippetTokens); err != nil {
			return Result{}, err
		}
	}
	var sem []int64
	available := true
	vectors, err := embedTexts(ctx, embedURL, []string{embed.QueryText(query)})
	if err == nil {
		if len(vectors) == 0 {
			err = fmt.Errorf("%w: list index out of range", httpx.ErrRange)
		} else {
			sem, err = semantic(ctx, q, vectors[0], f, lim.branchLimit, lim.knnMax)
		}
	}
	if err != nil {
		var unavailable *httpx.ServiceUnavailable
		if !errors.As(err, &unavailable) && !errors.Is(err, httpx.ErrPayload) && !errors.Is(err, httpx.ErrMissing) {
			return Result{}, err
		}
		available = false
		sem = nil
	}
	marked := map[int64]string{}
	lexIDs := make([]int64, len(lex))
	for i, h := range lex {
		lexIDs[i] = h.id
		marked[h.id] = h.snippet
	}
	fused := textsearch.RRFWith(lim.rrfK, lexIDs, sem)
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
	heads, err := bodyHead(ctx, q, unmarked, lim.snippetChars)
	if err != nil {
		return Result{}, err
	}
	hits := []Hit{}
	for _, s := range fused {
		i, ok := info[s.ID]
		if !ok {
			continue
		}
		var m *string
		if text, ok := marked[s.ID]; ok {
			m = &text
		}
		hits = append(hits, newHit(i, heads[s.ID], m))
	}
	return Result{Hits: hits, Semantic: available}, nil
}
