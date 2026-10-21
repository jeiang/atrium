package notion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/search"
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/py"
)

const Account = "notion"

const hitSelect = "SELECT rid, id, title, path, url, last_edited_time, edited_ts, private FROM notion_pages " +
	"WHERE in_trash = 0 AND rid IN (SELECT value FROM json_each(?))"

type Querier = search.Querier

type PathPart struct {
	ID    *string `json:"id"`
	Title string  `json:"title"`
}

type PageRef struct {
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	URL     string     `json:"url"`
	Path    []PathPart `json:"path"`
	Private bool       `json:"private"`
}

func pathParts(path string) []PathPart {
	parts := []PathPart{}
	for _, t := range strings.Split(path, PathSeparator) {
		if t != "" {
			parts = append(parts, PathPart{Title: t})
		}
	}
	return parts
}

func pageRef(id, title, url, path string, private bool) PageRef {
	return PageRef{ID: id, Title: title, URL: url, Path: pathParts(path), Private: private}
}

type Page struct {
	RID            int64   `json:"rid"`
	ID             string  `json:"id"`
	ParentType     string  `json:"parent_type"`
	ParentID       string  `json:"parent_id"`
	DataSourceID   string  `json:"data_source_id"`
	Title          string  `json:"title"`
	Path           string  `json:"path"`
	URL            string  `json:"url"`
	Properties     string  `json:"properties"`
	Markdown       string  `json:"markdown"`
	LastEditedTime string  `json:"last_edited_time"`
	EditedTS       float64 `json:"edited_ts"`
	InTrash        int64   `json:"in_trash"`
	Sensitivity    string  `json:"sensitivity"`
	Private        int64   `json:"private"`
	Links          string  `json:"links"`
	Embedded       int64   `json:"embedded"`
	SyncedAt       float64 `json:"synced_at"`
}

func GetPage(ctx context.Context, q Querier, pageID string) (*Page, error) {
	var p Page
	err := q.QueryRowContext(ctx,
		"SELECT rid, id, parent_type, parent_id, data_source_id, title, path, url, properties, markdown, last_edited_time, "+
			"edited_ts, in_trash, sensitivity, private, links, embedded, synced_at FROM notion_pages WHERE id = ?",
		notionid.Normalize(pageID)).
		Scan(&p.RID, &p.ID, &p.ParentType, &p.ParentID, &p.DataSourceID, &p.Title, &p.Path, &p.URL, &p.Properties,
			&p.Markdown, &p.LastEditedTime, &p.EditedTS, &p.InTrash, &p.Sensitivity, &p.Private, &p.Links, &p.Embedded, &p.SyncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

type PropertyRow struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func propertyText(prop py.Dict) string {
	kind, _ := field(prop, "type").(string)
	value := field(prop, kind)
	switch kind {
	case "title", "rich_text":
		items, _ := value.([]any)
		return richText(items)
	case "select", "status":
		return text(asDict(value), "name")
	case "multi_select":
		var names []string
		items, _ := value.([]any)
		for _, v := range items {
			names = append(names, text(asDict(v), "name"))
		}
		return strings.Join(names, ", ")
	case "date":
		d := asDict(value)
		var parts []string
		for _, k := range []string{"start", "end"} {
			if s := field(d, k); py.Truthy(s) {
				parts = append(parts, pyStr(s))
			}
		}
		return strings.Join(parts, " → ")
	case "relation":
		items, _ := value.([]any)
		if len(items) > 0 {
			return strconv.Itoa(len(items)) + " linked"
		}
		return ""
	case "people":
		var names []string
		items, _ := value.([]any)
		for _, v := range items {
			names = append(names, text(asDict(v), "name"))
		}
		return strings.Join(names, ", ")
	}
	switch value.(type) {
	case nil, py.Dict, []any:
		return ""
	}
	return pyStr(value)
}

func propertyRows(properties string) ([]PropertyRow, error) {
	v, err := py.JSONLoads([]byte(properties))
	if err != nil {
		return nil, err
	}
	rows := []PropertyRow{}
	for _, p := range asDict(v) {
		prop := asDict(p.Value)
		if text(prop, "type") == "title" {
			continue
		}
		if value := propertyText(prop); value != "" {
			rows = append(rows, PropertyRow{Name: p.Key, Value: value})
		}
	}
	return rows, nil
}

type View struct {
	PageRef
	EditedTS   float64       `json:"edited_ts"`
	Properties []PropertyRow `json:"properties"`
	Markdown   string        `json:"markdown"`
	InTrash    bool          `json:"in_trash"`
}

func PageView(ctx context.Context, q Querier, pageID string) (*View, error) {
	page, err := GetPage(ctx, q, pageID)
	if page == nil || err != nil {
		return nil, err
	}
	rows, err := propertyRows(page.Properties)
	if err != nil {
		return nil, err
	}
	return &View{
		PageRef:    pageRef(page.ID, page.Title, page.URL, page.Path, page.Private != 0),
		EditedTS:   page.EditedTS,
		Properties: rows,
		Markdown:   page.Markdown,
		InTrash:    page.InTrash != 0,
	}, nil
}

func visibilities(includePrivate bool) []string {
	if includePrivate {
		return []string{embed.VisibilityPublic, embed.VisibilityPrivate}
	}
	return []string{embed.VisibilityPublic}
}

type nearRow struct {
	rid      int64
	distance float64
}

func knn(ctx context.Context, q Querier, blob []byte, k int, includePrivate bool) ([]nearRow, error) {
	var total int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM notion_vec").Scan(&total); err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, nil
	}
	k = min(k, total, embed.KNNMax)
	var out []nearRow
	for _, visibility := range visibilities(includePrivate) {
		rows, err := q.QueryContext(ctx,
			"SELECT rowid, distance FROM notion_vec WHERE embedding MATCH ? AND k = ? AND visibility = ?", blob, k, visibility)
		if err != nil {
			return nil, err
		}
		got, err := db.Collect(rows, func(r *sql.Rows) (nearRow, error) {
			var n nearRow
			err := r.Scan(&n.rid, &n.distance)
			return n, err
		})
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].distance < out[j].distance })
	return out, nil
}

type lexicalHit struct {
	rid     int64
	snippet string
}

func lexical(ctx context.Context, q Querier, match string, includePrivate bool, limit int) ([]lexicalHit, error) {
	rows, err := q.QueryContext(ctx,
		fmt.Sprintf("SELECT p.rid, snippet(notion_fts, 2, '%s', '%s', '…', %d) AS snip ", markStart, markEnd, search.SnippetTokens)+
			"FROM notion_fts JOIN notion_pages p ON p.rid = notion_fts.rowid "+
			"WHERE notion_fts MATCH ? AND p.in_trash = 0 AND (? = 1 OR p.private = 0) "+
			"ORDER BY bm25(notion_fts, 5.0, 2.0, 1.0) LIMIT ?",
		match, b2i(includePrivate), limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (lexicalHit, error) {
		var h lexicalHit
		err := r.Scan(&h.rid, &h.snippet)
		return h, err
	})
}

func semantic(ctx context.Context, q Querier, vector []float64, includePrivate bool, limit int) ([]int64, error) {
	rows, err := knn(ctx, q, embed.Serialize(vector), limit, includePrivate)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.rid)
	}
	return out[:min(len(out), limit)], nil
}

type hitInfo struct {
	rid                  int64
	id, title, path, url string
	lastEditedTime       string
	editedTS             float64
	private              bool
	score                float64
}

func idsJSON[T int64 | string](list []T) string {
	raw, _ := json.Marshal(list)
	return string(raw)
}

func hydrate(ctx context.Context, q Querier, rids []int64) (map[int64]hitInfo, error) {
	out := map[int64]hitInfo{}
	if len(rids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, hitSelect, idsJSON(rids))
	if err != nil {
		return nil, err
	}
	list, err := db.Collect(rows, scanInfo)
	for _, i := range list {
		out[i.rid] = i
	}
	return out, err
}

func scanInfo(r *sql.Rows) (hitInfo, error) {
	var i hitInfo
	var private int64
	err := r.Scan(&i.rid, &i.id, &i.title, &i.path, &i.url, &i.lastEditedTime, &i.editedTS, &private)
	i.private = private != 0
	return i, err
}

func PageTitles(ctx context.Context, q Querier, texts []string) (map[string]string, error) {
	ids := notionid.IDsInText(strings.Join(texts, " "))
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	rows, err := q.QueryContext(ctx,
		"SELECT id, title FROM notion_pages WHERE in_trash = 0 AND id IN (SELECT value FROM json_each(?))", idsJSON(ids))
	if err != nil {
		return nil, err
	}
	type titled struct{ id, title string }
	list, err := db.Collect(rows, func(r *sql.Rows) (titled, error) {
		var t titled
		err := r.Scan(&t.id, &t.title)
		return t, err
	})
	out := make(map[string]string, len(list))
	for _, t := range list {
		out[t.id] = t.title
	}
	return out, err
}

func heads(ctx context.Context, q Querier, limits SearchLimits, rids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(rids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx,
		"SELECT rid, substr(markdown, 1, ?) AS head FROM notion_pages WHERE rid IN (SELECT value FROM json_each(?))",
		limits.HeadSource, idsJSON(rids))
	if err != nil {
		return nil, err
	}
	type head struct {
		rid  int64
		text string
	}
	list, err := db.Collect(rows, func(r *sql.Rows) (head, error) {
		var h head
		err := r.Scan(&h.rid, &h.text)
		return h, err
	})
	if err != nil {
		return nil, err
	}
	texts := make([]string, len(list))
	for i, h := range list {
		texts[i] = h.text
	}
	titles, err := PageTitles(ctx, q, texts)
	if err != nil {
		return nil, err
	}
	for _, h := range list {
		out[h.rid] = py.Clip(Plain(h.text, titles), limits.Snippet)
	}
	return out, nil
}

type Hit struct {
	PageRef
	Account        string        `json:"account"`
	Subject        string        `json:"subject"`
	Folder         string        `json:"folder"`
	LastEditedTime string        `json:"last_edited_time"`
	EditedTS       float64       `json:"edited_ts"`
	DateTS         float64       `json:"date_ts"`
	FromAddr       string        `json:"from_addr"`
	FromName       string        `json:"from_name"`
	Seen           bool          `json:"seen"`
	Flagged        bool          `json:"flagged"`
	HasAttachment  bool          `json:"has_attachment"`
	Score          *float64      `json:"score"`
	LexicalRank    *int          `json:"lexical_rank"`
	SemanticRank   *int          `json:"semantic_rank"`
	Snippet        string        `json:"snippet"`
	SnippetParts   []search.Part `json:"snippet_parts"`
}

func newHit(info hitInfo, score *float64, lexRank, semRank *int, head string, marked *string) Hit {
	parts := []search.Part{}
	switch {
	case marked != nil && *marked != "":
		parts = search.SnippetParts(*marked)
	case head != "":
		parts = []search.Part{{Text: head}}
	}
	var snippet strings.Builder
	for _, p := range parts {
		snippet.WriteString(p.Text)
	}
	return Hit{
		PageRef: pageRef(info.id, info.title, info.url, info.path, info.private), Account: Account, Subject: info.title,
		Folder: info.path, LastEditedTime: info.lastEditedTime, EditedTS: info.editedTS, DateTS: info.editedTS,
		Seen: true, Score: score, LexicalRank: lexRank, SemanticRank: semRank, Snippet: snippet.String(), SnippetParts: parts,
	}
}

func recent(ctx context.Context, q Querier, limits SearchLimits, includePrivate bool, limit int) ([]Hit, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT rid, id, title, path, url, last_edited_time, edited_ts, private FROM notion_pages "+
			"WHERE in_trash = 0 AND (? = 1 OR private = 0) ORDER BY edited_ts DESC, rid DESC LIMIT ?",
		b2i(includePrivate), limit)
	if err != nil {
		return nil, err
	}
	infos, err := db.Collect(rows, scanInfo)
	if err != nil {
		return nil, err
	}
	rids := make([]int64, len(infos))
	for i, info := range infos {
		rids[i] = info.rid
	}
	head, err := heads(ctx, q, limits, rids)
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, len(infos))
	for i, info := range infos {
		hits[i] = newHit(info, nil, nil, nil, head[info.rid], nil)
	}
	return hits, nil
}

type Filters struct {
	Account        string `json:"account"`
	IncludePrivate bool   `json:"include_private"`
}

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

func Search(ctx context.Context, q db.Querier, query string, includePrivate bool, limit int, embedTexts embed.TextsFunc) (Result, error) {
	if embedTexts == nil {
		embedTexts = embed.Texts
	}
	limits, err := LoadSearch(ctx, q)
	if err != nil {
		return Result{}, err
	}
	query = py.Strip(query)
	filters := Filters{Account: Account, IncludePrivate: includePrivate}
	if query == "" {
		hits, err := recent(ctx, q, limits, includePrivate, limit)
		return Result{Hits: hits, Filters: filters}, err
	}
	match, err := search.FTSQuery(query)
	if err != nil {
		return Result{}, err
	}
	var lex []lexicalHit
	if match != "" {
		if lex, err = lexical(ctx, q, match, includePrivate, limits.Branch); err != nil {
			return Result{}, err
		}
	}
	var sem []int64
	var failure *string
	available := true
	embedURL, err := mail.EmbedURL(ctx, q)
	if err != nil {
		return Result{}, err
	}
	modelCtx, err := mail.WithModels(ctx, q)
	if err != nil {
		return Result{}, err
	}
	vectors, err := embedTexts(modelCtx, embedURL, []string{embed.QueryText(query)})
	if err == nil {
		if len(vectors) == 0 {
			err = fmt.Errorf("%w: list index out of range", httpx.ErrIndex)
		} else {
			sem, err = semantic(ctx, q, vectors[0], includePrivate, limits.Branch)
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
	lexIDs := make([]int64, len(lex))
	snippets := make([]string, len(lex))
	for i, h := range lex {
		lexRank[h.rid] = i + 1
		lexIDs[i] = h.rid
		snippets[i] = h.snippet
	}
	titles, err := PageTitles(ctx, q, snippets)
	if err != nil {
		return Result{}, err
	}
	marked := map[int64]string{}
	for _, h := range lex {
		marked[h.rid] = Plain(h.snippet, titles)
	}
	semRank := rank(sem)
	fused := search.RRF(lexIDs, sem)
	fused = fused[:min(len(fused), max(limit, 0))]
	rids := make([]int64, len(fused))
	for i, s := range fused {
		rids[i] = s.ID
	}
	info, err := hydrate(ctx, q, rids)
	if err != nil {
		return Result{}, err
	}
	var unmarked []int64
	for _, rid := range rids {
		if _, ok := marked[rid]; !ok {
			unmarked = append(unmarked, rid)
		}
	}
	head, err := heads(ctx, q, limits, unmarked)
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
		if t, ok := marked[s.ID]; ok {
			m = &t
		}
		score := s.Score
		hits = append(hits, newHit(i, &score, lr, sr, head[s.ID], m))
	}
	return Result{Hits: hits, Semantic: available, Error: failure, Filters: filters}, nil
}

type Related struct {
	PageRef
	Linked bool    `json:"linked"`
	Score  float64 `json:"score"`
}

func RelatedPages(ctx context.Context, q Querier, pageID string, limit int) ([]Related, error) {
	page, err := GetPage(ctx, q, pageID)
	if page == nil || err != nil {
		return []Related{}, err
	}
	allowPrivate := page.Private != 0
	var linkIDs []any
	if v, err := py.JSONLoads([]byte(page.Links)); err == nil {
		linkIDs, _ = v.([]any)
	} else {
		return nil, err
	}
	var linked []string
	for _, id := range linkIDs {
		s, _ := id.(string)
		linked = append(linked, notionid.Normalize(s))
	}
	rows, err := q.QueryContext(ctx,
		"SELECT p.id FROM notion_pages p, json_each(p.links) j WHERE j.value = ? AND p.id != ?", page.ID, page.ID)
	if err != nil {
		return nil, err
	}
	backlinks, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var id string
		err := r.Scan(&id)
		return id, err
	})
	if err != nil {
		return nil, err
	}
	out := []Related{}
	taken := map[string]bool{page.ID: true}
	for _, id := range slices.Concat(linked, backlinks) {
		if taken[id] {
			continue
		}
		var info hitInfo
		var private int64
		err := q.QueryRowContext(ctx, "SELECT rid, id, title, path, url, private FROM notion_pages WHERE id = ? AND in_trash = 0", id).
			Scan(&info.rid, &info.id, &info.title, &info.path, &info.url, &private)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && private != 0 && !allowPrivate) {
			continue
		}
		if err != nil {
			return nil, err
		}
		taken[id] = true
		out = append(out, Related{PageRef: pageRef(info.id, info.title, info.url, info.path, private != 0), Linked: true, Score: 1.0})
	}
	var blob []byte
	err = q.QueryRowContext(ctx, "SELECT embedding FROM notion_vec WHERE rowid = ?", page.RID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return out[:min(len(out), limit)], nil
	}
	if err != nil {
		return nil, err
	}
	near, err := knn(ctx, q, blob, limit+len(taken)+1, allowPrivate)
	if err != nil {
		return nil, err
	}
	rids := make([]int64, len(near))
	for i, n := range near {
		rids[i] = n.rid
	}
	info, err := hydrate(ctx, q, rids)
	if err != nil {
		return nil, err
	}
	for _, n := range near {
		row, ok := info[n.rid]
		if !ok || taken[row.id] {
			continue
		}
		taken[row.id] = true
		out = append(out, Related{PageRef: pageRef(row.id, row.title, row.url, row.path, row.private), Score: max(0, 1.0-n.distance)})
	}
	return out[:min(len(out), limit)], nil
}
