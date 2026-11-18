package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

var (
	citeToken     = regexp.MustCompile(`\[K([1-9][0-9]{0,15})\]`)
	markdownLink  = regexp.MustCompile(`\]\(\s*<?([^\s<>()]+)>?(?:\s+(?:"[^"]*"|'[^']*'))?\s*\)`)
	codeText      = regexp.MustCompile("(?s)```.*?(?:```|$)|~~~.*?(?:~~~|$)|`[^`\n]*`")
	knowledgePath = regexp.MustCompile(`^/knowledge/([1-9][0-9]{0,15})/?$`)
)

func Cited(body, origin string) []int64 {
	text := codeText.ReplaceAllString(body, " ")
	var base *url.URL
	if origin != "" {
		if u, err := url.Parse(origin); err == nil && u.Host != "" {
			base = u
		}
	}
	seen := map[int64]bool{}
	var out []int64
	add := func(raw string) {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, m := range citeToken.FindAllStringSubmatchIndex(text, -1) {
		if m[1] < len(text) && text[m[1]] == '(' {
			continue
		}
		add(text[m[2]:m[3]])
	}
	for _, m := range markdownLink.FindAllStringSubmatch(text, -1) {
		u, err := url.Parse(m[1])
		if err != nil {
			continue
		}
		switch {
		case u.Scheme == "" && u.Host == "":
		case base != nil && strings.EqualFold(u.Scheme, base.Scheme) && strings.EqualFold(u.Host, base.Host):
		default:
			continue
		}
		if p := knowledgePath.FindStringSubmatch(u.Path); p != nil {
			add(p[1])
		}
	}
	return out
}

func CitedRecords(ctx context.Context, q db.Querier, from Record, origin string) ([]Record, error) {
	if from.Retracted() {
		return nil, nil
	}
	ids := []int64{}
	for _, id := range Cited(from.Body, origin) {
		if id != from.ID {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM kb_records r WHERE r.retracted_at IS NULL AND r.id IN (SELECT value FROM json_each(?))", string(raw))
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (Record, error) { return scan(r) })
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]Record, len(found))
	for _, r := range found {
		byID[r.ID] = r
	}
	out := make([]Record, 0, len(found))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func CitingRecords(ctx context.Context, q db.Querier, to Record, origin string) ([]Record, error) {
	if to.Retracted() {
		return nil, nil
	}
	id := to.ID
	n := strconv.FormatInt(id, 10)
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM kb_records r JOIN kb_fts ON kb_fts.rowid = r.id "+
		"WHERE kb_fts MATCH ? AND r.retracted_at IS NULL AND r.id != ? ORDER BY r.id",
		`body : k`+n+` OR body : "knowledge `+n+`"`, id)
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (Record, error) { return scan(r) })
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(found))
	for _, r := range found {
		for _, cited := range Cited(r.Body, origin) {
			if cited == id {
				out = append(out, r)
				break
			}
		}
	}
	return out, nil
}
