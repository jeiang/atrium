package notion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/py"
)

var log = logx.Logger("atrium.notion.sync")

const (
	sensitivityProperty = "sensitivity"
	privateValue        = "private"
	PathSeparator       = " / "
	untitled            = "Untitled"
	maxPathDepth        = 12
)

var noMarkdownStatuses = [...]int{400, 403, 404}

// Stats is SyncStats, in the order the command prints it.
type Stats struct {
	Sources   int
	Pages     int
	New       int
	Updated   int
	Unchanged int
	Trashed   int
	Failed    int
	Embedded  int
	Seconds   float64
}

// Counts is counts: the mirror's totals.
type Counts struct {
	Pages    int64 `json:"pages"`
	Live     int64 `json:"live"`
	Private  int64 `json:"private"`
	Embedded int64 `json:"embedded"`
	Sources  int64 `json:"sources"`
}

// PageTitle is page_title.
func PageTitle(page py.Dict) string {
	for _, p := range asDict(field(page, "properties")) {
		prop := asDict(p.Value)
		if text(prop, "type") == "title" {
			return py.Strip(richText(list(prop, "title")))
		}
	}
	return ""
}

func sourceTitle(source py.Dict) string { return py.Strip(richText(list(source, "title"))) }

// SensitivityOf is sensitivity_of.
func SensitivityOf(properties py.Dict) string {
	for _, p := range properties {
		if py.Lower(py.Strip(p.Key)) != sensitivityProperty {
			continue
		}
		prop := asDict(p.Value)
		kind, _ := field(prop, "type").(string)
		value := field(prop, kind)
		switch kind {
		case "select", "status":
			return text(asDict(value), "name")
		case "multi_select":
			var names []string
			items, _ := value.([]any)
			for _, v := range items {
				names = append(names, text(asDict(v), "name"))
			}
			return strings.Join(names, ",")
		case "rich_text", "title":
			items, _ := value.([]any)
			return richText(items)
		}
	}
	return ""
}

// IsPrivateValue is is_private_value.
func IsPrivateValue(value string) bool {
	for _, v := range strings.Split(value, ",") {
		if py.Lower(py.Strip(v)) == privateValue {
			return true
		}
	}
	return false
}

func relationTargets(properties py.Dict) []string {
	var out []string
	for _, p := range properties {
		prop := asDict(p.Value)
		if text(prop, "type") != "relation" {
			continue
		}
		for _, item := range list(prop, "relation") {
			id := notionid.Normalize(text(asDict(item), "id"))
			if id != "" && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

func linkTargets(properties py.Dict, markdown string) []string {
	out := relationTargets(properties)
	for _, id := range notionid.IDsInText(markdown) {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func parentRef(parent py.Dict) (kind, id string) {
	kind = text(parent, "type")
	if kind == "workspace" {
		return kind, ""
	}
	return kind, notionid.Normalize(text(parent, kind))
}

func editedTS(value string) float64 {
	dt, ok := py.FromISOFormat(strings.ReplaceAll(value, "Z", "+00:00"))
	if !ok {
		return 0
	}
	return dt.Timestamp(time.Local)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func upsertSource(ctx context.Context, q db.Querier, source py.Dict) error {
	parent := asDict(field(source, "database_parent"))
	if !py.Truthy(parent) {
		parent = asDict(field(source, "parent"))
	}
	kind, parentID := parentRef(parent)
	databaseID := notionid.Normalize(text(asDict(field(source, "parent")), "database_id"))
	props := field(source, "properties")
	if !py.Truthy(props) {
		props = py.Dict{}
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO notion_sources (id, database_id, parent_type, parent_id, title, url, properties, "+
			"last_edited_time, in_trash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET database_id = excluded.database_id, parent_type = excluded.parent_type, "+
			"parent_id = excluded.parent_id, title = excluded.title, url = excluded.url, "+
			"properties = excluded.properties, last_edited_time = excluded.last_edited_time, "+
			"in_trash = excluded.in_trash",
		notionid.Normalize(text(source, "id")), databaseID, kind, parentID, sourceTitle(source), text(source, "url"),
		py.JSONDumps(props), text(source, "last_edited_time"), b2i(py.Truthy(field(source, "in_trash"))))
	return err
}

// FetchMarkdown is fetch_markdown: the page text from the markdown endpoint, or from its blocks when the
// endpoint refuses the page.
func FetchMarkdown(ctx context.Context, client *Client, pageID string) (string, error) {
	reply, err := client.PageMarkdown(ctx, pageID)
	if err == nil {
		return text(reply, "markdown"), nil
	}
	var refused *Error
	if !errors.As(err, &refused) || !slices.Contains(noMarkdownStatuses[:], refused.Status) {
		return "", err
	}
	log.Info("markdown endpoint refused page; using blocks")
	return client.BlocksMarkdown(ctx, pageID, 0)
}

func ftsWrite(ctx context.Context, q db.Querier, rid int64, title, path, markdown string) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM notion_fts WHERE rowid = ?", rid); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "INSERT INTO notion_fts (rowid, title, path, body) VALUES (?, ?, ?, ?)",
		rid, title, path, py.Clip(markdown, config.FTSBodyChars))
	return err
}

func dropIndex(ctx context.Context, q db.Querier, rid int64) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM notion_fts WHERE rowid = ?", rid); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "DELETE FROM notion_vec WHERE rowid = ?", rid)
	return err
}

func upsertPage(ctx context.Context, q db.Querier, client *Client, page py.Dict, stats *Stats, now float64) error {
	pid := notionid.Normalize(text(page, "id"))
	props := asDict(field(page, "properties"))
	edited := text(page, "last_edited_time")
	var rid, trashedBefore int64
	var storedEdited, storedMarkdown string
	found := true
	err := q.QueryRowContext(ctx, "SELECT rid, last_edited_time, markdown, in_trash FROM notion_pages WHERE id = ?", pid).
		Scan(&rid, &storedEdited, &storedMarkdown, &trashedBefore)
	if errors.Is(err, sql.ErrNoRows) {
		found = false
	} else if err != nil {
		return err
	}
	parent := asDict(field(page, "parent"))
	kind, parentID := parentRef(parent)
	dataSource := ""
	if kind == "data_source_id" {
		dataSource = notionid.Normalize(text(parent, "data_source_id"))
	}
	trashed := int64(b2i(py.Truthy(field(page, "in_trash"))))
	if found && storedEdited == edited && trashedBefore == trashed {
		_, err := q.ExecContext(ctx, "UPDATE notion_pages SET synced_at = ? WHERE rid = ?", now, rid)
		stats.Unchanged++
		return err
	}
	markdown := ""
	reuse := found && storedEdited == edited && storedMarkdown != ""
	switch {
	case reuse:
		markdown = storedMarkdown
	case trashed == 0:
		markdown, err = FetchMarkdown(ctx, client, pid)
		if err != nil {
			return err
		}
	}
	sens := SensitivityOf(props)
	values := []py.Pair{
		{Key: "parent_type", Value: kind},
		{Key: "parent_id", Value: parentID},
		{Key: "data_source_id", Value: dataSource},
		{Key: "title", Value: PageTitle(page)},
		{Key: "url", Value: text(page, "url")},
		{Key: "properties", Value: py.JSONDumps(props)},
		{Key: "markdown", Value: markdown},
		{Key: "last_edited_time", Value: edited},
		{Key: "edited_ts", Value: editedTS(edited)},
		{Key: "in_trash", Value: trashed},
		{Key: "sensitivity", Value: sens},
		{Key: "private", Value: b2i(IsPrivateValue(sens))},
		{Key: "links", Value: py.JSONDumps(linkTargets(props, markdown))},
		{Key: "embedded", Value: 0},
		{Key: "synced_at", Value: now},
	}
	if !found {
		columns, marks, args := []string{"id"}, []string{"?"}, []any{pid}
		for _, v := range values {
			columns = append(columns, v.Key)
			marks = append(marks, "?")
			args = append(args, v.Value)
		}
		if _, err := q.ExecContext(ctx, fmt.Sprintf("INSERT INTO notion_pages (%s) VALUES (%s)",
			strings.Join(columns, ", "), strings.Join(marks, ", ")), args...); err != nil {
			return err
		}
		stats.New++
	} else {
		sets, args := make([]string, len(values)), make([]any, 0, len(values)+1)
		for i, v := range values {
			sets[i] = v.Key + " = ?"
			args = append(args, v.Value)
		}
		args = append(args, pid)
		if _, err := q.ExecContext(ctx, "UPDATE notion_pages SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "DELETE FROM notion_fts WHERE rowid = ?", rid); err != nil {
			return err
		}
		stats.Updated++
	}
	if trashed != 0 {
		if err := q.QueryRowContext(ctx, "SELECT rid FROM notion_pages WHERE id = ?", pid).Scan(&rid); err != nil {
			return err
		}
		if err := dropIndex(ctx, q, rid); err != nil {
			return err
		}
		stats.Trashed++
	}
	return nil
}

type pathNode struct {
	id, kind, parent, title, sensitivity string
}

type sourceNode struct {
	id, databaseID, kind, parent, title string
}

func resolvePaths(ctx context.Context, q db.Querier) error {
	rows, err := q.QueryContext(ctx, "SELECT id, parent_type, parent_id, title, sensitivity FROM notion_pages WHERE in_trash = 0")
	if err != nil {
		return err
	}
	order, err := db.Collect(rows, func(r *sql.Rows) (pathNode, error) {
		var n pathNode
		err := r.Scan(&n.id, &n.kind, &n.parent, &n.title, &n.sensitivity)
		return n, err
	})
	if err != nil {
		return err
	}
	pages := make(map[string]pathNode, len(order))
	for _, n := range order {
		pages[n.id] = n
	}
	rows, err = q.QueryContext(ctx, "SELECT id, database_id, parent_type, parent_id, title FROM notion_sources")
	if err != nil {
		return err
	}
	sourceList, err := db.Collect(rows, func(r *sql.Rows) (sourceNode, error) {
		var n sourceNode
		err := r.Scan(&n.id, &n.databaseID, &n.kind, &n.parent, &n.title)
		return n, err
	})
	if err != nil {
		return err
	}
	sources := make(map[string]sourceNode, len(sourceList))
	byDatabase := map[string]sourceNode{}
	for _, s := range sourceList {
		sources[s.id] = s
	}
	for _, s := range sourceList {
		if s.databaseID != "" {
			byDatabase[s.databaseID] = s
		}
	}

	chain := func(kind, pid string) ([]string, bool) {
		var titles []string
		private := false
		type key struct{ kind, id string }
		seen := map[key]bool{}
		for len(titles) < maxPathDepth && !seen[key{kind, pid}] {
			seen[key{kind, pid}] = true
			if node, ok := pages[pid]; ok && kind == "page_id" {
				titles = append(titles, orUntitled(node.title))
				private = private || IsPrivateValue(node.sensitivity)
				kind, pid = node.kind, node.parent
				continue
			}
			if kind != "data_source_id" && kind != "database_id" {
				break
			}
			node, ok := sources[pid]
			if !ok {
				if node, ok = byDatabase[pid]; !ok {
					break
				}
			}
			titles = append(titles, orUntitled(node.title))
			kind, pid = node.kind, node.parent
		}
		slices.Reverse(titles)
		return titles, private
	}

	type change struct {
		id      string
		path    string
		private int
	}
	changed := make([]change, 0, len(order))
	for _, page := range order {
		titles, inherited := chain(page.kind, page.parent)
		changed = append(changed, change{page.id, strings.Join(titles, PathSeparator), b2i(IsPrivateValue(page.sensitivity) || inherited)})
	}
	for _, c := range changed {
		var rid, private int64
		var path, title, markdown string
		if err := q.QueryRowContext(ctx, "SELECT rid, path, private, title, markdown FROM notion_pages WHERE id = ?", c.id).
			Scan(&rid, &path, &private, &title, &markdown); err != nil {
			return err
		}
		if path != c.path || private != int64(c.private) {
			if _, err := q.ExecContext(ctx,
				"UPDATE notion_pages SET path = ?, private = ?, embedded = CASE WHEN private != ? THEN 0 ELSE embedded END WHERE rid = ?",
				c.path, c.private, c.private, rid); err != nil {
				return err
			}
		}
		indexed := true
		if path == c.path {
			var one int
			err := q.QueryRowContext(ctx, "SELECT 1 FROM notion_fts WHERE rowid = ?", rid).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				indexed = false
			} else if err != nil {
				return err
			}
		}
		if path != c.path || !indexed {
			if err := ftsWrite(ctx, q, rid, title, c.path, markdown); err != nil {
				return err
			}
		}
	}
	return nil
}

func orUntitled(title string) string {
	if title == "" {
		return untitled
	}
	return title
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Sync is sync: the workspace's data sources and pages into the mirror tables, paths and privacy resolved, and
// the pages that changed embedded. An embeddings server that is down leaves them for the next run.
func Sync(ctx context.Context, d *db.DB, cfg config.Config, client *Client, embedTexts embed.TextsFunc) (Stats, error) {
	if embedTexts == nil {
		embedTexts = embed.Texts
	}
	started := time.Now()
	stamp := now()
	var stats Stats
	err := client.Search(ctx, "data_source", false, func(source py.Dict) error {
		if text(source, "object") != "data_source" {
			return nil
		}
		if err := upsertSource(ctx, d.Write, source); err != nil {
			return err
		}
		stats.Sources++
		return nil
	})
	if err != nil {
		return stats, err
	}
	seen := map[string]bool{}
	err = client.Search(ctx, "page", false, func(page py.Dict) error {
		if text(page, "object") != "page" {
			return nil
		}
		stats.Pages++
		seen[notionid.Normalize(text(page, "id"))] = true
		err := upsertPage(ctx, d.Write, client, page, &stats, stamp)
		var refused *Error
		if errors.As(err, &refused) {
			stats.Failed++
			log.Warn(fmt.Sprintf("page %s: %s", text(page, "id"), err))
			return nil
		}
		return err
	})
	if err != nil {
		return stats, err
	}
	rows, err := d.Write.QueryContext(ctx, "SELECT rid, id FROM notion_pages WHERE in_trash = 0")
	if err != nil {
		return stats, err
	}
	type live struct {
		rid int64
		id  string
	}
	pages, err := db.Collect(rows, func(r *sql.Rows) (live, error) {
		var l live
		err := r.Scan(&l.rid, &l.id)
		return l, err
	})
	if err != nil {
		return stats, err
	}
	for _, p := range pages {
		if seen[p.id] {
			continue
		}
		if _, err := d.Write.ExecContext(ctx, "UPDATE notion_pages SET in_trash = 1, embedded = 1 WHERE rid = ?", p.rid); err != nil {
			return stats, err
		}
		if err := dropIndex(ctx, d.Write, p.rid); err != nil {
			return stats, err
		}
		stats.Trashed++
	}
	if err := resolvePaths(ctx, d.Write); err != nil {
		return stats, err
	}
	if _, err := d.Write.ExecContext(ctx,
		"INSERT INTO notion_state (key, value) VALUES ('last_sync_at', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		py.FloatRepr(stamp)); err != nil {
		return stats, err
	}
	embedded, err := embed.NotionPending(ctx, d, cfg.EmbedURL, embedTexts)
	if err != nil {
		var unavailable *httpx.ServiceUnavailable
		if !errors.As(err, &unavailable) && !errors.Is(err, httpx.ErrValue) && !errors.Is(err, httpx.ErrKey) {
			return stats, err
		}
		log.Warn("embedding unavailable: " + httpx.Message(err))
	} else {
		stats.Embedded = embedded
	}
	stats.Seconds = time.Since(started).Seconds()
	return stats, nil
}

// Totals is counts.
func Totals(ctx context.Context, q db.Querier) (Counts, error) {
	var c Counts
	err := q.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(in_trash = 0), 0), COALESCE(SUM(private = 1 AND in_trash = 0), 0), "+
			"COALESCE(SUM(embedded = 1 AND in_trash = 0), 0) FROM notion_pages").Scan(&c.Pages, &c.Live, &c.Private, &c.Embedded)
	if err != nil {
		return c, err
	}
	err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM notion_sources").Scan(&c.Sources)
	return c, err
}

// Pending is pending_count: the live pages not embedded yet.
func Pending(ctx context.Context, q db.Querier) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM notion_pages WHERE embedded = 0 AND in_trash = 0").Scan(&n)
	return n, err
}
