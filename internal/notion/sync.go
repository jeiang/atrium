package notion

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
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/notionid"
)

var log = logx.Logger("atrium.notion.sync")

const (
	PathSeparator = " / "
	untitled      = "Untitled"
	maxPathDepth  = 12
)

var noMarkdownStatuses = [...]int{400, 403, 404}

type Stats struct {
	New       int
	Updated   int
	Unchanged int
	Trashed   int
	Failed    int
	Seconds   float64
}

type Counts struct {
	Pages    int64 `json:"pages"`
	Live     int64 `json:"live"`
	Private  int64 `json:"private"`
	Embedded int64 `json:"embedded"`
	Sources  int64 `json:"sources"`
}

func (s Sensitivity) Of(properties map[string]Property) string {
	if s.Property == "" {
		return ""
	}
	for name, prop := range properties {
		if strings.ToLower(strings.TrimSpace(name)) != s.Property {
			continue
		}
		switch prop.Type {
		case "select", "status", "multi_select", "rich_text", "title", "formula", "rollup":
			return prop.Text()
		}
		return s.Private
	}
	return s.Private
}

func (s Sensitivity) IsPrivate(value string) bool {
	for _, v := range strings.Split(value, ",") {
		if strings.ToLower(strings.TrimSpace(v)) == s.Private {
			return true
		}
	}
	return false
}

func relationTargets(properties map[string]Property) []string {
	var out []string
	for _, prop := range properties {
		if prop.Type != "relation" {
			continue
		}
		for _, item := range prop.Relation {
			id := notionid.Normalize(item.ID)
			if id != "" && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

func linkTargets(properties map[string]Property, markdown string) []string {
	out := relationTargets(properties)
	for _, id := range notionid.IDsInText(markdown) {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func editedTS(value string) float64 {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0
	}
	return float64(t.UnixNano()) / 1e9
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func upsertSource(ctx context.Context, q db.Querier, source SearchResult) error {
	parent := source.DatabaseParent
	if parent.Type == "" {
		parent = source.Parent
	}
	kind, parentID := parent.Ref()
	databaseID := notionid.Normalize(source.Parent.DatabaseID)
	props := source.Properties
	if props == nil {
		props = map[string]Property{}
	}
	encoded, err := json.Marshal(props)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		"INSERT INTO notion_sources (id, database_id, parent_type, parent_id, title, url, properties, "+
			"last_edited_time, in_trash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET database_id = excluded.database_id, parent_type = excluded.parent_type, "+
			"parent_id = excluded.parent_id, title = excluded.title, url = excluded.url, "+
			"properties = excluded.properties, last_edited_time = excluded.last_edited_time, "+
			"in_trash = excluded.in_trash",
		notionid.Normalize(source.ID), databaseID, kind, parentID, source.TitleText(), source.URL,
		string(encoded), source.LastEditedTime, b2i(source.InTrash))
	return err
}

func FetchMarkdown(ctx context.Context, client *Client, pageID string) (string, error) {
	reply, err := client.PageMarkdown(ctx, pageID)
	if err == nil {
		return reply.Markdown, nil
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
		rid, title, path, clip(markdown, config.FTSBodyChars))
	return err
}

func dropIndex(ctx context.Context, q db.Querier, rid int64) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM notion_fts WHERE rowid = ?", rid); err != nil {
		return err
	}
	return embed.DeleteNotion(ctx, q, rid)
}

func upsertPage(ctx context.Context, q db.Querier, client *Client, sensitivity Sensitivity, page RemotePage, stats *Stats, now float64) error {
	pid := notionid.Normalize(page.ID)
	props := page.Properties
	edited := page.LastEditedTime
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
	kind, parentID := page.Parent.Ref()
	dataSource := ""
	if kind == "data_source_id" {
		dataSource = notionid.Normalize(page.Parent.DataSourceID)
	}
	trashed := int64(b2i(page.InTrash))
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
	sens := sensitivity.Of(props)
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return err
	}
	linksJSON, err := json.Marshal(linkTargets(props, markdown))
	if err != nil {
		return err
	}
	values := []column{
		{name: "parent_type", value: kind},
		{name: "parent_id", value: parentID},
		{name: "data_source_id", value: dataSource},
		{name: "title", value: page.Title()},
		{name: "url", value: page.URL},
		{name: "properties", value: string(propsJSON)},
		{name: "markdown", value: markdown},
		{name: "last_edited_time", value: edited},
		{name: "edited_ts", value: editedTS(edited)},
		{name: "in_trash", value: trashed},
		{name: "sensitivity", value: sens},
		{name: "private", value: b2i(sensitivity.IsPrivate(sens))},
		{name: "links", value: string(linksJSON)},
		{name: "embedded", value: 0},
		{name: "synced_at", value: now},
	}
	if !found {
		columns, marks, args := []string{"id"}, []string{"?"}, []any{pid}
		for _, v := range values {
			columns = append(columns, v.name)
			marks = append(marks, "?")
			args = append(args, v.value)
		}
		if _, err := q.ExecContext(ctx, fmt.Sprintf("INSERT INTO notion_pages (%s) VALUES (%s)",
			strings.Join(columns, ", "), strings.Join(marks, ", ")), args...); err != nil {
			return err
		}
		stats.New++
	} else {
		sets, args := make([]string, len(values)), make([]any, 0, len(values)+1)
		for i, v := range values {
			sets[i] = v.name + " = ?"
			args = append(args, v.value)
		}
		args = append(args, pid)
		if _, err := q.ExecContext(ctx, "UPDATE notion_pages SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "DELETE FROM notion_fts WHERE rowid = ?", rid); err != nil {
			return err
		}
		if err := embed.StaleNotion(ctx, q, rid); err != nil {
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

type column struct {
	name  string
	value any
}

type pathNode struct {
	id, kind, parent, title, sensitivity string
}

type sourceNode struct {
	id, databaseID, kind, parent, title string
}

func resolvePaths(ctx context.Context, q db.Querier, sensitivity Sensitivity) error {
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
				private = private || sensitivity.IsPrivate(node.sensitivity)
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
		changed = append(changed, change{page.id, strings.Join(titles, PathSeparator), b2i(sensitivity.IsPrivate(page.sensitivity) || inherited)})
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

func Sync(ctx context.Context, d *db.DB, client *Client, embedTexts embed.TextsFunc) (Stats, error) {
	if embedTexts == nil {
		embedTexts = embed.Texts
	}
	sensitivity, err := LoadSensitivity(ctx, d.Read)
	if err != nil {
		return Stats{}, err
	}
	started := time.Now()
	stamp := now()
	var stats Stats
	err = client.Search(ctx, "data_source", func(source SearchResult) error {
		if source.Object != "data_source" {
			return nil
		}
		return upsertSource(ctx, d.Write, source)
	})
	if err != nil {
		return stats, err
	}
	seen := map[string]bool{}
	err = client.Search(ctx, "page", func(page SearchResult) error {
		if page.Object != "page" {
			return nil
		}
		seen[notionid.Normalize(page.ID)] = true
		err := upsertPage(ctx, d.Write, client, sensitivity, page.RemotePage(), &stats, stamp)
		var refused *Error
		if errors.As(err, &refused) {
			stats.Failed++
			log.Warn(fmt.Sprintf("page %s: %s", page.ID, err))
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
	if err := resolvePaths(ctx, d.Write, sensitivity); err != nil {
		return stats, err
	}
	embedURL, err := mail.EmbedURL(ctx, d.Read)
	if err != nil {
		return stats, err
	}
	modelCtx, err := mail.WithModels(ctx, d.Read)
	if err != nil {
		return stats, err
	}
	_, err = embed.NotionPending(modelCtx, d, embedURL, embedTexts)
	if err != nil {
		var unavailable *httpx.ServiceUnavailable
		if !errors.As(err, &unavailable) && !errors.Is(err, httpx.ErrPayload) && !errors.Is(err, httpx.ErrMissing) {
			return stats, err
		}
		log.Warn("embedding unavailable: " + httpx.Message(err))
	}
	stats.Seconds = time.Since(started).Seconds()
	return stats, nil
}

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
