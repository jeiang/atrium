package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/tool"
)

const MaxImportBodyChars = 100000

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	phoneRe = regexp.MustCompile(`\+?[0-9][0-9\s().\-]{7,}[0-9]`)
)

func hasPII(text string) bool {
	if emailRe.MatchString(text) {
		return true
	}
	for _, match := range phoneRe.FindAllString(text, -1) {
		digits := 0
		for _, r := range match {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits >= 9 {
			return true
		}
	}
	return false
}

type SourceCounts struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Kept    int `json:"kept"`
	Failed  int `json:"failed"`
}

type ImportStats struct {
	DryRun          bool         `json:"dry_run"`
	Profile         SourceCounts `json:"profile"`
	Observations    SourceCounts `json:"observations"`
	Notion          SourceCounts `json:"notion"`
	PII             int          `json:"pii"`
	Private         int          `json:"private"`
	ChangedInAtrium []string     `json:"changed_in_atrium"`
	Missing         []string     `json:"missing"`
	FailedItems     []string     `json:"failed_items"`
}

type importItem struct {
	Origin    string
	Title     string
	Aliases   []string
	Body      string
	Scope     string
	Private   bool
	Marks     tool.Marks
	Pinned    bool
	Confirmed bool
	Retracted bool
}

func day(epoch float64) string { return time.Unix(int64(epoch), 0).UTC().Format("2006-01-02") }

func profileItems(ctx context.Context, q db.Querier) ([]importItem, error) {
	rows, err := q.QueryContext(ctx, "SELECT key, value FROM agent_profile ORDER BY key")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (importItem, error) {
		var key, value string
		err := r.Scan(&key, &value)
		return importItem{Origin: "profile:" + key, Title: key, Body: value, Scope: ScopePersonal, Pinned: true, Confirmed: true}, err
	})
}

func observationItems(ctx context.Context, q db.Querier) ([]importItem, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT subject, value, first_seen, last_seen, count FROM nightly_observations ORDER BY subject, last_seen DESC, value")
	if err != nil {
		return nil, err
	}
	type observation struct {
		subject, value string
		first, last    float64
		count          int64
	}
	list, err := db.Collect(rows, func(r *sql.Rows) (observation, error) {
		var o observation
		err := r.Scan(&o.subject, &o.value, &o.first, &o.last, &o.count)
		return o, err
	})
	if err != nil {
		return nil, err
	}
	latest := map[string]bool{}
	out := make([]importItem, 0, len(list))
	for _, o := range list {
		body := fmt.Sprintf("%s (seen %d times, %s to %s)", o.value, o.count, day(o.first), day(o.last))
		item := importItem{
			Origin: "observation:" + o.subject + "=" + o.value, Title: o.subject, Body: body,
			Scope: ScopePersonal, Marks: tool.Untrusted,
		}
		if latest[o.subject] {
			item.Retracted = true
		} else {
			latest[o.subject] = true
		}
		out = append(out, item)
	}
	return out, nil
}

type notionRow struct {
	id, parent, title, markdown, aliases string
	private                              int64
}

func notionItems(ctx context.Context, q db.Querier) ([]importItem, error) {
	wiki, err := notion.LoadWiki(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx,
		"SELECT p.id, p.parent_id, p.title, p.markdown, p.private, COALESCE(w.aliases, '[]') "+
			"FROM notion_pages p LEFT JOIN wiki_pages w ON w.page_id = p.id WHERE p.in_trash = 0 AND p.markdown != ''")
	if err != nil {
		return nil, err
	}
	pages, err := db.Collect(rows, func(r *sql.Rows) (notionRow, error) {
		var p notionRow
		err := r.Scan(&p.id, &p.parent, &p.title, &p.markdown, &p.private, &p.aliases)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	children := map[string][]string{}
	listed := map[string]bool{}
	for _, p := range pages {
		children[p.parent] = append(children[p.parent], p.id)
	}
	rows, err = q.QueryContext(ctx, "SELECT page_id FROM wiki_pages")
	if err != nil {
		return nil, err
	}
	ids, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var id string
		err := r.Scan(&id)
		return id, err
	})
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		listed[id] = true
	}
	under := map[string]bool{}
	queue := append([]string{}, children[wiki.Root]...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if under[id] {
			continue
		}
		under[id] = true
		queue = append(queue, children[id]...)
	}
	out := []importItem{}
	for _, p := range pages {
		if !under[p.id] && !listed[p.id] {
			continue
		}
		body, _, sources, err := notion.SplitFooter(p.markdown)
		if err != nil {
			return nil, fmt.Errorf("notion page %s: %w", p.id, err)
		}
		if strings.TrimSpace(body) == "" {
			continue
		}
		if len(sources) > 0 {
			body += "\n\nSources: " + strings.Join(sources, ", ")
		}
		aliases := []string{}
		_ = json.Unmarshal([]byte(p.aliases), &aliases)
		title := p.title
		if strings.TrimSpace(title) == "" {
			title = "Untitled"
		}
		marks := tool.Untrusted
		if hasPII(body) {
			marks |= tool.PII
		}
		out = append(out, importItem{
			Origin: "notion:" + p.id, Title: title, Aliases: aliases, Body: body,
			Scope: ScopePersonal, Private: p.private != 0, Marks: marks,
		})
	}
	return out, nil
}

type existingRecord struct {
	id          int64
	title       string
	aliases     string
	body        string
	scope       string
	private     int64
	marks       int64
	pinned      int64
	confirmedAt *float64
	retractedAt *float64
}

func existingByOrigin(ctx context.Context, q db.Querier) (map[string]existingRecord, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, origin, title, aliases, body, scope, private, marks, pinned, confirmed_at, retracted_at FROM kb_records WHERE origin IS NOT NULL")
	if err != nil {
		return nil, err
	}
	type found struct {
		origin string
		rec    existingRecord
	}
	list, err := db.Collect(rows, func(r *sql.Rows) (found, error) {
		var f found
		err := r.Scan(&f.rec.id, &f.origin, &f.rec.title, &f.rec.aliases, &f.rec.body, &f.rec.scope,
			&f.rec.private, &f.rec.marks, &f.rec.pinned, &f.rec.confirmedAt, &f.rec.retractedAt)
		return f, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]existingRecord, len(list))
	for _, f := range list {
		out[f.origin] = f.rec
	}
	return out, nil
}

func writtenTargets(ctx context.Context, q db.Querier) (map[int64]bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT target FROM agent_writes WHERE target LIKE 'kb:%'")
	if err != nil {
		return nil, err
	}
	targets, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var target string
		err := r.Scan(&target)
		return target, err
	})
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, target := range targets {
		var id int64
		if _, err := fmt.Sscanf(target, "kb:%d", &id); err == nil {
			out[id] = true
		}
	}
	return out, nil
}

func sameItem(rec existingRecord, item importItem) bool {
	aliases, _ := aliasesJSON(item.Aliases)
	confirmed := rec.confirmedAt != nil
	retracted := rec.retractedAt != nil
	return rec.title == item.Title && rec.aliases == aliases && rec.body == item.Body && rec.scope == item.Scope &&
		(rec.private != 0) == item.Private && tool.Marks(rec.marks) == item.Marks &&
		(rec.pinned != 0) == item.Pinned && confirmed == item.Confirmed && retracted == item.Retracted
}

func importOne(ctx context.Context, tx *sql.Tx, item importItem, rec *existingRecord, now float64) (string, error) {
	aliases, err := aliasesJSON(item.Aliases)
	if err != nil {
		return "", err
	}
	var confirmed, retracted any
	if item.Confirmed {
		confirmed = now
	}
	if item.Retracted {
		retracted = now
	}
	if rec == nil {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO kb_records (title, aliases, body, scope, private, marks, writer, origin, pinned, confirmed_at, retracted_at, embedded, created_at, updated_at) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)",
			item.Title, aliases, item.Body, item.Scope, b2i(item.Private), int64(item.Marks), WriterImport, item.Origin,
			b2i(item.Pinned), confirmed, retracted, now, now)
		return "created", err
	}
	_, err = tx.ExecContext(ctx,
		"UPDATE kb_records SET title = ?, aliases = ?, body = ?, scope = ?, private = ?, marks = ?, pinned = ?, confirmed_at = ?, "+
			"retracted_at = ?, embedded = 0, updated_at = ? WHERE id = ?",
		item.Title, aliases, item.Body, item.Scope, b2i(item.Private), int64(item.Marks), b2i(item.Pinned), confirmed,
		retracted, now, rec.id)
	return "updated", err
}

func Import(ctx context.Context, d *db.DB, dryRun bool, now float64) (ImportStats, error) {
	stats := ImportStats{DryRun: dryRun, ChangedInAtrium: []string{}, Missing: []string{}, FailedItems: []string{}}
	sources := []struct {
		counts *SourceCounts
		items  []importItem
	}{}
	profile, err := profileItems(ctx, d.Read)
	if err != nil {
		return stats, err
	}
	observations, err := observationItems(ctx, d.Read)
	if err != nil {
		return stats, err
	}
	notionPages, err := notionItems(ctx, d.Read)
	if err != nil {
		return stats, err
	}
	sources = append(sources, struct {
		counts *SourceCounts
		items  []importItem
	}{&stats.Profile, profile}, struct {
		counts *SourceCounts
		items  []importItem
	}{&stats.Observations, observations}, struct {
		counts *SourceCounts
		items  []importItem
	}{&stats.Notion, notionPages})

	existing, err := existingByOrigin(ctx, d.Read)
	if err != nil {
		return stats, err
	}
	written, err := writtenTargets(ctx, d.Read)
	if err != nil {
		return stats, err
	}
	seen := map[string]bool{}
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, source := range sources {
			for _, item := range source.items {
				seen[item.Origin] = true
				if len(item.Body) > MaxImportBodyChars {
					source.counts.Failed++
					stats.FailedItems = append(stats.FailedItems, fmt.Sprintf("%s: body exceeds %d characters", item.Origin, MaxImportBodyChars))
					continue
				}
				rec, found := existing[item.Origin]
				if found {
					if written[rec.id] {
						source.counts.Kept++
						stats.ChangedInAtrium = append(stats.ChangedInAtrium, item.Origin)
						continue
					}
					if sameItem(rec, item) {
						source.counts.Kept++
						continue
					}
					if !dryRun {
						if _, err := importOne(ctx, tx, item, &rec, now); err != nil {
							return err
						}
					}
					source.counts.Updated++
				} else {
					if !dryRun {
						if _, err := importOne(ctx, tx, item, nil, now); err != nil {
							return err
						}
					}
					source.counts.Created++
				}
				if item.Marks&tool.PII != 0 {
					stats.PII++
				}
				if item.Private {
					stats.Private++
				}
			}
		}
		return nil
	})
	if err != nil {
		return stats, err
	}
	for origin := range existing {
		if !seen[origin] {
			stats.Missing = append(stats.Missing, origin)
		}
	}
	return stats, nil
}
