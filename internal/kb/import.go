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
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/notionmd"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/writes"
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
	Notes           []string     `json:"notes"`
	ChangedInAtrium []string     `json:"changed_in_atrium"`
	Missing         []string     `json:"missing"`
	FailedItems     []string     `json:"failed_items"`
}

type importItem struct {
	Origin    string
	NotionID  string
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

func notionItems(ctx context.Context, q db.Querier) ([]importItem, bool, error) {
	wiki, err := notion.LoadWiki(ctx, q)
	if err != nil {
		return nil, false, err
	}
	pages, err := notion.ImportPages(ctx, q)
	if err != nil {
		return nil, false, err
	}
	children := map[string][]string{}
	listed := map[string]bool{}
	for _, p := range pages {
		children[p.Parent] = append(children[p.Parent], p.ID)
	}
	ids, err := notion.WikiPageIDs(ctx, q)
	if err != nil {
		return nil, false, err
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
	privatePages := 0
	for _, p := range pages {
		if !under[p.ID] && !listed[p.ID] {
			continue
		}
		body, _, sources, err := notion.SplitFooter(p.Markdown)
		if err != nil {
			return nil, false, fmt.Errorf("notion page %s: %w", p.ID, err)
		}
		if strings.TrimSpace(body) == "" {
			continue
		}
		if len(sources) > 0 {
			body += "\n\nSources: " + strings.Join(sources, ", ")
		}
		aliases := []string{}
		_ = json.Unmarshal([]byte(p.Aliases), &aliases)
		title := p.Title
		if strings.TrimSpace(title) == "" {
			title = "Untitled"
		}
		if p.Private != 0 {
			privatePages++
		}
		out = append(out, importItem{
			Origin: "notion:" + p.ID, NotionID: notionid.Normalize(p.ID), Title: title, Aliases: aliases, Body: body,
			Scope: ScopePersonal, Private: p.Private != 0, Marks: tool.Untrusted,
		})
	}
	uniform := len(out) > 0 && privatePages == len(out)
	if uniform {
		for i := range out {
			out[i].Private = false
		}
	}
	return out, uniform, nil
}

func notionTitles(ctx context.Context, q db.Querier) (map[string]string, error) {
	pages, err := notion.Titles(ctx, q)
	if err != nil {
		return nil, err
	}
	titles := make(map[string]string, len(pages))
	for id, title := range pages {
		titles[notionid.Normalize(id)] = title
	}
	return titles, nil
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
	targets, err := writes.TargetsLike(ctx, q, "kb:%", "kb_confirm")
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

func effectiveItem(rec existingRecord, item importItem) importItem {
	if rec.confirmedAt != nil {
		item.Confirmed = true
		item.Marks &^= tool.Untrusted
	}
	return item
}

func sameItem(rec existingRecord, item importItem) bool {
	item = effectiveItem(rec, item)
	aliases, _ := aliasesJSON(item.Aliases)
	confirmed := rec.confirmedAt != nil
	retracted := rec.retractedAt != nil
	return rec.title == item.Title && rec.aliases == aliases && rec.body == item.Body && rec.scope == item.Scope &&
		(rec.private != 0) == item.Private && tool.Marks(rec.marks) == item.Marks &&
		(rec.pinned != 0) == item.Pinned && confirmed == item.Confirmed && retracted == item.Retracted
}

func importOne(ctx context.Context, tx *sql.Tx, item importItem, rec *existingRecord, now float64) (string, int64, error) {
	if rec != nil {
		item = effectiveItem(*rec, item)
	}
	aliases, err := aliasesJSON(item.Aliases)
	if err != nil {
		return "", 0, err
	}
	var confirmed, retracted any
	switch {
	case rec != nil && rec.confirmedAt != nil:
		confirmed = *rec.confirmedAt
	case item.Confirmed:
		confirmed = now
	}
	if item.Retracted {
		retracted = now
	}
	if rec == nil {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO kb_records (title, aliases, body, scope, private, marks, writer, origin, pinned, confirmed_at, retracted_at, embedded, created_at, updated_at) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)",
			item.Title, aliases, item.Body, item.Scope, b2i(item.Private), int64(item.Marks), WriterImport, item.Origin,
			b2i(item.Pinned), confirmed, retracted, now, now)
		if err != nil {
			return "", 0, err
		}
		id, err := res.LastInsertId()
		return "created", id, err
	}
	_, err = tx.ExecContext(ctx,
		"UPDATE kb_records SET title = ?, aliases = ?, body = ?, scope = ?, private = ?, marks = ?, pinned = ?, confirmed_at = ?, "+
			"retracted_at = ?, embedded = 0, updated_at = ? WHERE id = ?",
		item.Title, aliases, item.Body, item.Scope, b2i(item.Private), int64(item.Marks), b2i(item.Pinned), confirmed,
		retracted, now, rec.id)
	return "updated", rec.id, err
}

func notionRefToken(id string) string { return "\x1fkb:" + id + "\x1f" }

func Import(ctx context.Context, d *db.DB, dryRun bool, now float64) (ImportStats, error) {
	stats := ImportStats{DryRun: dryRun, Notes: []string{}, ChangedInAtrium: []string{}, Missing: []string{}, FailedItems: []string{}}
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
	notionPages, uniformPrivate, err := notionItems(ctx, d.Read)
	if err != nil {
		return stats, err
	}
	titles, err := notionTitles(ctx, d.Read)
	if err != nil {
		return stats, err
	}
	if uniformPrivate {
		stats.Notes = append(stats.Notes, "notion: every page under the wiki root is private, so private is treated as a sync default rather than an operator choice; imported records are not private")
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
	rawBodies := make([]string, len(notionPages))
	refs := make(map[string]string, len(notionPages))
	for i, item := range notionPages {
		rawBodies[i] = item.Body
		if rec, ok := existing[item.Origin]; ok {
			refs[item.NotionID] = "[" + Cite(rec.id) + "]"
		} else {
			refs[item.NotionID] = notionRefToken(item.NotionID)
		}
	}
	convertNotion := func() {
		for i := range notionPages {
			notionPages[i].Body = notionmd.Import(rawBodies[i], refs, titles)
		}
	}
	convertNotion()
	failed := map[string]bool{}
	for _, item := range notionPages {
		if len(item.Body) > MaxImportBodyChars {
			failed[item.NotionID] = true
		}
	}
	if len(failed) > 0 {
		for id := range failed {
			delete(refs, id)
		}
		convertNotion()
	}
	for i := range notionPages {
		notionPages[i].Marks = tool.Untrusted
		if hasPII(notionPages[i].Body) {
			notionPages[i].Marks |= tool.PII
		}
	}
	seen := map[string]bool{}
	recordIDs := map[string]int64{}
	pending := map[string]bool{}
	confirmedUpdated := 0
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
					if item.NotionID != "" {
						recordIDs[item.NotionID] = rec.id
					}
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
						_, id, err := importOne(ctx, tx, item, &rec, now)
						if err != nil {
							return err
						}
						if item.NotionID != "" {
							recordIDs[item.NotionID] = id
							pending[item.Origin] = true
						}
					}
					if item.NotionID != "" && rec.confirmedAt != nil {
						confirmedUpdated++
					}
					source.counts.Updated++
				} else {
					if !dryRun {
						_, id, err := importOne(ctx, tx, item, nil, now)
						if err != nil {
							return err
						}
						if item.NotionID != "" {
							recordIDs[item.NotionID] = id
							pending[item.Origin] = true
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
		if dryRun {
			return nil
		}
		for _, item := range notionPages {
			if !pending[item.Origin] {
				continue
			}
			id, ok := recordIDs[item.NotionID]
			if !ok {
				continue
			}
			body := item.Body
			for pageID, recordID := range recordIDs {
				body = strings.ReplaceAll(body, notionRefToken(pageID), "["+Cite(recordID)+"]")
			}
			if body == item.Body {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE kb_records SET body = ?, embedded = 0, updated_at = ? WHERE id = ?", body, now, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return stats, err
	}
	if confirmedUpdated > 0 {
		verb := "updated"
		if dryRun {
			verb = "would be updated"
		}
		stats.Notes = append(stats.Notes, fmt.Sprintf("notion: %d confirmed records %s and kept confirmed; an operator confirm is not a content edit", confirmedUpdated, verb))
	}
	for origin := range existing {
		if !seen[origin] {
			stats.Missing = append(stats.Missing, origin)
		}
	}
	return stats, nil
}
