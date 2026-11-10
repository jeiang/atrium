package export

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/kb/render"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/settings"
)

type Result struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Trashed int `json:"trashed"`
	Skipped int `json:"skipped"`
}

func Run(ctx context.Context, d *db.DB, env config.Env, now float64) (Result, error) {
	var out Result
	read := func(key string) (string, error) {
		found, err := settings.From(ctx).Resolve(ctx, d.Read, key)
		return strings.TrimSpace(found.Value), err
	}
	enabled, err := read(NotionEnabledKey)
	if err != nil {
		return out, err
	}
	if enabled != "true" {
		return out, nil
	}
	parent, err := read(NotionParentKey)
	if err != nil {
		return out, err
	}
	parent = notionid.Normalize(parent)
	if parent == "" {
		return out, fmt.Errorf("export: %s is not set", NotionParentKey)
	}
	if err := blocked(ctx, d.Read); err != nil {
		return out, err
	}
	privateTo, err := read(NotionPrivateKey)
	if err != nil {
		return out, err
	}
	allow, err := read(NotionKindsKey)
	if err != nil {
		return out, err
	}
	records, err := kb.Exportable(ctx, d.Read, kb.ExportFilter{
		Now:            now,
		Kinds:          mask(allow),
		IncludePrivate: privateTo != "" && notionid.Normalize(privateTo) == parent,
	})
	if err != nil {
		return out, err
	}
	loc, err := config.From(ctx).Location(env)
	if err != nil {
		return out, err
	}
	base := config.From(ctx).PublicURL(env)
	titles, err := threadTitles(ctx, d.Read, records)
	if err != nil {
		return out, err
	}
	entries := make([]pageEntry, 0, len(records)+1)
	for _, rec := range records {
		src := render.Source{PublicURL: base, Date: date(rec.UpdatedAt, loc)}
		if rec.SourceThreadID != nil {
			src.ThreadTitle = titles[*rec.SourceThreadID]
		}
		entries = append(entries, pageEntry{recordID: rec.ID, page: render.RecordPage(rec.Record, src)})
	}
	entries = append(entries, pageEntry{recordID: 0, page: render.Index(indexEntries(records, loc))})
	client, err := notion.NewClient(ctx, d.Read, env)
	if err != nil {
		return out, err
	}
	return syncNotion(ctx, d, now, client, parent, entries)
}

func threadTitles(ctx context.Context, q db.Querier, records []kb.ExportRecord) (map[int64]string, error) {
	out := make(map[int64]string)
	var ids []any
	seen := make(map[int64]bool, len(records))
	for _, rec := range records {
		if rec.SourceThreadID == nil || seen[*rec.SourceThreadID] {
			continue
		}
		seen[*rec.SourceThreadID] = true
		ids = append(ids, *rec.SourceThreadID)
	}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := q.QueryContext(ctx, "SELECT id, title FROM threads WHERE id IN ("+placeholders+")", ids...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out[id] = title
	}
	return out, rows.Err()
}

func indexEntries(records []kb.ExportRecord, loc *time.Location) []render.Entry {
	out := make([]render.Entry, 0, len(records))
	for _, rec := range records {
		out = append(out, render.Entry{
			Cite: kb.Cite(rec.ID), Title: rec.Title, Scope: rec.Scope,
			Writer: rec.Writer, Updated: date(rec.UpdatedAt, loc),
		})
	}
	return out
}

func date(ts float64, loc *time.Location) string {
	return time.Unix(int64(ts), 0).In(loc).Format("2006-01-02")
}
