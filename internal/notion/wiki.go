package notion

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	logStateKey  = "wiki_log_page"
	FooterPrefix = "Last reviewed "
)

var (
	footerRe    = rx.MustCompile(`^Last reviewed (\d{4}-\d{2}-\d{2})(?: · sources: (.*))?\s*$`, 0)
	wikiLinkRe  = rx.MustCompile(`\[([^\]]*)\]\(([^)\s]+)\)`, 0)
	wikiHeadRe  = rx.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`, 0)
	wikiURLRe   = rx.MustCompile(`^[a-z][a-z0-9+.-]*://`, rx.IgnoreCase)
	aliasSplit  = rx.MustCompile(`[,;\n]`, 0)
	labelSchema = rx.MustCompile(`^[a-z][a-z0-9+.-]*://(www\.)?`, rx.IgnoreCase)
)

type WikiError string

func (e WikiError) Error() string { return string(e) }

func (WikiError) PyClass() string { return "WikiError" }

func (WikiError) PyModule() string { return "atrium.notion_wiki" }

func LocalDate(ts float64, env config.Env) (string, error) {
	loc, err := config.Location(env)
	if err != nil {
		return "", err
	}
	sec, us := py.SplitTimestamp(ts)
	return time.Unix(sec, us*1000).In(loc).Format("2006-01-02"), nil
}

func NormalizeKey(value string) string {
	return strings.Join(py.Split(py.Casefold(value)), " ")
}

func stringList(values any, what string) ([]string, error) {
	items, ok := values.([]any)
	if !ok {
		return nil, WikiError(what)
	}
	out := make([]string, len(items))
	for i, v := range items {
		s, ok := v.(string)
		if !ok {
			return nil, WikiError(what)
		}
		out[i] = s
	}
	return out, nil
}

func (w Wiki) CleanAliases(values any) ([]string, error) {
	var raw []string
	if s, ok := values.(string); ok {
		parts, err := aliasSplit.Split(s, 0)
		if err != nil {
			return nil, err
		}
		raw = parts
	} else {
		var err error
		if raw, err = stringList(values, "aliases must be a list of strings"); err != nil {
			return nil, err
		}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, v := range raw {
		text := strings.Join(py.Split(v), " ")
		if text != "" && !seen[NormalizeKey(text)] {
			seen[NormalizeKey(text)] = true
			out = append(out, text)
		}
	}
	return out[:min(len(out), w.MaxAliases)], nil
}

func (w Wiki) CleanSources(values any) ([]string, error) {
	if values == nil {
		return []string{}, nil
	}
	raw, err := stringList(values, "sources must be a list of strings (URLs or short labels)")
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, v := range raw {
		if py.Strip(v) == "" {
			continue
		}
		if text := strings.Join(py.Split(v), " "); !slices.Contains(out, text) {
			out = append(out, text)
		}
	}
	return out[:min(len(out), w.MaxSources)], nil
}

type WikiPage struct {
	PageID       string
	Title        string
	HubID        string
	Aliases      []string
	LastReviewed string
	Sources      []string
	InTrash      bool
	UpdatedAt    float64
}

func decodeStrings(text string) ([]string, error) {
	v, err := py.JSONLoads([]byte(text))
	if err != nil {
		return nil, err
	}
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out, nil
}

func GetWiki(ctx context.Context, q db.Querier, pageID string) (*WikiPage, error) {
	var w WikiPage
	var aliases, sources string
	var trash int64
	err := q.QueryRowContext(ctx,
		"SELECT page_id, title, hub_id, aliases, last_reviewed, sources, in_trash, updated_at FROM wiki_pages WHERE page_id = ?",
		notionid.Normalize(pageID)).Scan(&w.PageID, &w.Title, &w.HubID, &aliases, &w.LastReviewed, &sources, &trash, &w.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if w.Aliases, err = decodeStrings(aliases); err != nil {
		return nil, err
	}
	if w.Sources, err = decodeStrings(sources); err != nil {
		return nil, err
	}
	w.InTrash = trash != 0
	return &w, nil
}

type WikiFields struct {
	Title        *string
	HubID        *string
	Aliases      []string
	SetAliases   bool
	LastReviewed *string
	Sources      []string
	SetSources   bool
	InTrash      *bool
}

func UpsertWiki(ctx context.Context, q db.Querier, pageID string, now float64, f WikiFields) error {
	columns := []string{"page_id"}
	args := []any{notionid.Normalize(pageID)}
	add := func(name string, value any) {
		columns = append(columns, name)
		args = append(args, value)
	}
	if f.Title != nil {
		add("title", *f.Title)
	}
	if f.HubID != nil {
		add("hub_id", *f.HubID)
	}
	if f.SetAliases {
		add("aliases", py.JSONDumps(orEmpty(f.Aliases)))
	}
	if f.LastReviewed != nil {
		add("last_reviewed", *f.LastReviewed)
	}
	if f.SetSources {
		add("sources", py.JSONDumps(orEmpty(f.Sources)))
	}
	if f.InTrash != nil {
		add("in_trash", b2i(*f.InTrash))
	}
	add("updated_at", now)
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(columns)), ", ")
	sets := make([]string, 0, len(columns)-1)
	for _, c := range columns[1:] {
		sets = append(sets, c+" = excluded."+c)
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO wiki_pages ("+strings.Join(columns, ", ")+") VALUES ("+marks+") ON CONFLICT(page_id) DO UPDATE SET "+strings.Join(sets, ", "),
		args...)
	return err
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func ParentOf(ctx context.Context, q db.Querier, pageID string) (string, error) {
	var parentType, parentID string
	var syncedAt float64
	mirrored := true
	err := q.QueryRowContext(ctx, "SELECT parent_type, parent_id, synced_at FROM notion_pages WHERE id = ? AND in_trash = 0", pageID).
		Scan(&parentType, &parentID, &syncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		mirrored = false
	} else if err != nil {
		return "", err
	}
	var hub string
	var updated float64
	err = q.QueryRowContext(ctx, "SELECT hub_id, updated_at FROM wiki_pages WHERE page_id = ? AND in_trash = 0", pageID).Scan(&hub, &updated)
	local := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if local && hub != "" && (!mirrored || updated >= syncedAt) {
		return hub, nil
	}
	if mirrored && parentType == "page_id" {
		return parentID, nil
	}
	return "", nil
}

func InTree(ctx context.Context, q db.Querier, pageID, root string) (bool, error) {
	seen := map[string]bool{}
	current := notionid.Normalize(pageID)
	for current != "" && !seen[current] {
		if current == root {
			return true, nil
		}
		seen[current] = true
		var err error
		if current, err = ParentOf(ctx, q, current); err != nil {
			return false, err
		}
	}
	return false, nil
}

type Existing struct {
	ID    string
	Title string
	URL   string
	Match string
}

func FindExisting(ctx context.Context, q db.Querier, title string, aliases []string) ([]Existing, error) {
	wanted := map[string]bool{}
	for _, k := range append([]string{NormalizeKey(title)}, mapKeys(aliases)...) {
		if k != "" {
			wanted[k] = true
		}
	}
	if len(wanted) == 0 {
		return []Existing{}, nil
	}
	rows, err := q.QueryContext(ctx,
		"SELECT w.page_id, w.title, w.aliases, p.title AS mirror_title, p.url, p.in_trash AS mirror_trash FROM wiki_pages w "+
			"LEFT JOIN notion_pages p ON p.id = w.page_id WHERE w.in_trash = 0")
	if err != nil {
		return nil, err
	}
	type localRow struct {
		id, title, aliases string
		mirrorTitle, url   sql.NullString
		mirrorTrash        sql.NullInt64
	}
	local, err := db.Collect(rows, func(r *sql.Rows) (localRow, error) {
		var l localRow
		err := r.Scan(&l.id, &l.title, &l.aliases, &l.mirrorTitle, &l.url, &l.mirrorTrash)
		return l, err
	})
	if err != nil {
		return nil, err
	}
	hits := []Existing{}
	for _, row := range local {
		if row.mirrorTrash.Int64 != 0 {
			continue
		}
		names, err := decodeStrings(row.aliases)
		if err != nil {
			return nil, err
		}
		pageTitle := row.title
		if row.mirrorTitle.String != "" {
			pageTitle = row.mirrorTitle.String
		}
		match := "alias"
		switch {
		case wanted[NormalizeKey(pageTitle)]:
			match = "title"
		case slices.ContainsFunc(names, func(n string) bool { return wanted[NormalizeKey(n)] }):
		default:
			continue
		}
		hits = append(hits, Existing{ID: row.id, Title: pageTitle, URL: row.url.String, Match: match})
	}
	rows, err = q.QueryContext(ctx,
		"SELECT id, title, url FROM notion_pages WHERE in_trash = 0 AND private = 0 AND id NOT IN (SELECT page_id FROM wiki_pages)")
	if err != nil {
		return nil, err
	}
	mirror, err := db.Collect(rows, func(r *sql.Rows) (Existing, error) {
		e := Existing{Match: "title"}
		err := r.Scan(&e.ID, &e.Title, &e.URL)
		return e, err
	})
	if err != nil {
		return nil, err
	}
	for _, e := range mirror {
		if wanted[NormalizeKey(e.Title)] {
			hits = append(hits, e)
		}
	}
	slices.SortStableFunc(hits, func(a, b Existing) int {
		if (a.Match != "title") != (b.Match != "title") {
			if a.Match != "title" {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Title, b.Title)
	})
	return hits, nil
}

func mapKeys(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = NormalizeKey(v)
	}
	return out
}

func AliasMatches(ctx context.Context, q db.Querier, query string) ([]string, error) {
	key := NormalizeKey(query)
	if key == "" {
		return []string{}, nil
	}
	rows, err := q.QueryContext(ctx, "SELECT page_id, title, aliases FROM wiki_pages WHERE in_trash = 0")
	if err != nil {
		return nil, err
	}
	type row struct{ id, title, aliases string }
	all, err := db.Collect(rows, func(r *sql.Rows) (row, error) {
		var x row
		err := r.Scan(&x.id, &x.title, &x.aliases)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, x := range all {
		names, err := decodeStrings(x.aliases)
		if err != nil {
			return nil, err
		}
		if key == NormalizeKey(x.title) || slices.ContainsFunc(names, func(n string) bool { return NormalizeKey(n) == key }) {
			out = append(out, x.id)
		}
	}
	return out, nil
}

func rstrip(s string) string { return strings.TrimRightFunc(s, py.IsSpace) }

func (w Wiki) linkLabel(url string) (string, error) {
	text, err := labelSchema.Sub("", url, 0)
	if err != nil {
		return "", err
	}
	text = strings.TrimRight(text, "/")
	if r := []rune(text); len(r) > w.LabelChars {
		return string(r[:w.LabelChars-1]) + "…", nil
	}
	return text, nil
}

func (w Wiki) renderSource(value string) (string, error) {
	m, err := wikiURLRe.Match(value)
	if err != nil {
		return "", err
	}
	if m != nil {
		label, err := w.linkLabel(value)
		return "[" + label + "](" + value + ")", err
	}
	return strings.ReplaceAll(value, ",", " "), nil
}

func parseSources(text string) ([]string, error) {
	var out []string
	found, err := wikiLinkRe.FindIter(text)
	if err != nil {
		return nil, err
	}
	for _, m := range found {
		out = append(out, m.Group(2))
	}
	rest, err := wikiLinkRe.Sub("", text, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(rest, ",") {
		if p := py.Strip(part); p != "" {
			out = append(out, p)
		}
	}
	return dedupe(out), nil
}

func dedupe(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func SplitFooter(markdown string) (body, reviewed string, sources []string, err error) {
	trimmed := rstrip(markdown)
	lines := strings.Split(trimmed, "\n")
	m, err := footerRe.Match(py.Strip(lines[len(lines)-1]))
	if err != nil {
		return "", "", nil, err
	}
	if m == nil {
		return trimmed, "", []string{}, nil
	}
	sources, err = parseSources(m.Group(2))
	if err != nil {
		return "", "", nil, err
	}
	if sources == nil {
		sources = []string{}
	}
	return rstrip(strings.Join(lines[:len(lines)-1], "\n")), m.Group(1), sources, nil
}

func (w Wiki) footerLine(date string, sources []string) (string, error) {
	line := FooterPrefix + date
	if len(sources) > 0 {
		rendered := make([]string, len(sources))
		for i, s := range sources {
			var err error
			if rendered[i], err = w.renderSource(s); err != nil {
				return "", err
			}
		}
		line += " · sources: " + strings.Join(rendered, ", ")
	}
	return line, nil
}

func (w Wiki) WithFooter(markdown, date string, sources []string) (string, []string, error) {
	body, _, previous, err := SplitFooter(markdown)
	if err != nil {
		return "", nil, err
	}
	merged := dedupe(slices.Concat(previous, sources))
	merged = merged[:min(len(merged), w.MaxSources)]
	line, err := w.footerLine(date, merged)
	if err != nil {
		return "", nil, err
	}
	if body != "" {
		line = body + "\n\n" + line
	}
	return line, merged, nil
}

func sectionSpan(markdown, heading string) (start, end int, ok bool, err error) {
	lines := strings.Split(markdown, "\n")
	wanted := NormalizeKey(heading)
	start, level := -1, 0
	for i, line := range lines {
		m, err := wikiHeadRe.Match(line)
		if err != nil {
			return 0, 0, false, err
		}
		if m == nil {
			continue
		}
		if start < 0 {
			if NormalizeKey(m.Group(2)) == wanted {
				start, level = i, len([]rune(m.Group(1)))
			}
		} else if len([]rune(m.Group(1))) <= level {
			return start, i, true, nil
		}
	}
	if start < 0 {
		return 0, 0, false, nil
	}
	return start, len(lines), true, nil
}

func ReplaceSection(markdown, heading, text string) (string, bool, error) {
	start, end, ok, err := sectionSpan(markdown, heading)
	if err != nil || !ok {
		return "", false, err
	}
	lines := strings.Split(markdown, "\n")
	out := slices.Concat(lines[:start+1], []string{"", py.Strip(text), ""}, lines[end:])
	joined := rstrip(strings.Join(out, "\n"))
	if strings.HasSuffix(markdown, "\n") {
		joined += "\n"
	}
	return joined, true, nil
}

func AppendToSection(markdown, heading, text string) (string, error) {
	_, end, ok, err := sectionSpan(markdown, heading)
	if err != nil {
		return "", err
	}
	if !ok {
		return strings.TrimRight(markdown, "\n") + "\n\n## " + py.Strip(heading) + "\n\n" + py.Strip(text), nil
	}
	lines := strings.Split(markdown, "\n")
	head := strings.TrimRight(strings.Join(lines[:end], "\n"), "\n")
	tail := strings.Join(lines[end:], "\n")
	out := head + "\n\n" + py.Strip(text)
	if py.Strip(tail) != "" {
		out += "\n\n" + tail
	}
	return out, nil
}

func EscapeLinkText(text string) string {
	if text == "" {
		text = "Untitled"
	}
	return strings.NewReplacer("[", "(", "]", ")").Replace(text)
}

func LogLine(date, action, title, url string) string {
	if url != "" {
		return "- " + date + " · " + action + " [" + EscapeLinkText(title) + "](" + url + ")"
	}
	return "- " + date + " · " + action + " " + EscapeLinkText(title)
}

func (w Wiki) LogPageID(ctx context.Context, d *db.DB, client *Client, root string) (string, error) {
	var id string
	err := d.Read.QueryRowContext(ctx, "SELECT value FROM notion_state WHERE key = ?", logStateKey).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	err = d.Read.QueryRowContext(ctx,
		"SELECT id FROM notion_pages WHERE in_trash = 0 AND parent_type = 'page_id' AND parent_id = ? AND title = ?", root, w.LogTitle).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		page, err := client.CreatePage(ctx, py.Dict{
			{Key: "parent", Value: py.Dict{{Key: "page_id", Value: root}}},
			{Key: "properties", Value: py.Dict{{Key: "title", Value: py.Dict{{Key: "title", Value: []any{
				py.Dict{{Key: "type", Value: "text"}, {Key: "text", Value: py.Dict{{Key: "content", Value: w.LogTitle}}}}}}}}}},
			{Key: "markdown", Value: ""},
		})
		if err != nil {
			return "", err
		}
		id = notionid.Normalize(text(page, "id"))
	} else if err != nil {
		return "", err
	}
	_, err = d.Write.ExecContext(ctx,
		"INSERT INTO notion_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", logStateKey, id)
	return id, err
}

func PrependLog(ctx context.Context, client *Client, logID string, lines []string) (string, error) {
	current, err := client.PageMarkdown(ctx, logID)
	if err != nil {
		return "", err
	}
	if py.Truthy(field(current, "truncated")) {
		return "", WikiError("the Wiki Log page is too large to update safely; archive it and let atrium start a new one")
	}
	previous := text(current, "markdown")
	updated := strings.Join(lines, "\n")
	if py.Strip(previous) != "" {
		updated += "\n" + strings.TrimLeft(previous, "\n")
	}
	_, err = client.UpdateMarkdown(ctx, logID, py.Dict{
		{Key: "type", Value: "replace_content"},
		{Key: "replace_content", Value: py.Dict{{Key: "new_str", Value: updated}}},
	})
	return previous, err
}
