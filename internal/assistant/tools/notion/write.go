package notiontools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/notionid"
)

const (
	undoRestore = "notion_page_restore"
	undoTrash   = "notion_page_trash"
	undoUntrash = "notion_page_untrash"
	undoMove    = "notion_page_move"
	undoAliases = "notion_aliases_restore"
)

var writeKinds = tool.Kinds{tool.Chat, tool.Nightly}

func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case bool:
		return v
	case int64:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func normalize(id string) string { return notionid.Normalize(id) }

func checkMarkdown(ctx context.Context, tc *tool.Context, markdown string) error {
	r := assistant.Read(ctx, tc.DB.Read)
	maxMarkdown := r.Int(assistant.KeyNotionMaxMarkdown)
	if err := r.Err(); err != nil {
		return err
	}
	if len([]rune(markdown)) > maxMarkdown {
		return tool.Error("markdown is limited to " + strconv.Itoa(maxMarkdown) + " characters")
	}
	urls, err := notion.ExternalURLs(markdown)
	if err != nil {
		return err
	}
	var blocked []string
	for _, u := range urls {
		if _, ok := tc.AllowedURLs[u]; !ok {
			blocked = append(blocked, u)
		}
	}
	if len(blocked) > 0 {
		return tool.Error("external image/embed URLs are not allowed in Notion writes (exfiltration guard): " + strings.Join(blocked[:min(3, len(blocked))], ", "))
	}
	return nil
}

func wikiOf(ctx context.Context, tc *tool.Context) (notion.Wiki, error) {
	wiki, err := notion.LoadWiki(ctx, tc.DB.Read)
	if err != nil {
		return notion.Wiki{}, err
	}
	if _, err := wiki.RootID(); err != nil {
		return notion.Wiki{}, tool.NotConfigured(err.Error())
	}
	return wiki, nil
}

func livePageOf(ctx context.Context, q db.Querier, client *notion.Client, pageID string) (notion.RemotePage, error) {
	page, err := client.GetPage(ctx, pageID)
	if err != nil {
		return notion.RemotePage{}, err
	}
	if page.InTrash {
		return notion.RemotePage{}, tool.Error("page is in the trash")
	}
	sensitivity, err := notion.LoadSensitivity(ctx, q)
	if err != nil {
		return notion.RemotePage{}, err
	}
	if sensitivity.IsPrivate(sensitivity.Of(page.Properties)) {
		return notion.RemotePage{}, tool.Error("page is private")
	}
	return page, nil
}

type summary struct{ pageID, title, url string }

func summarize(page notion.RemotePage) summary {
	return summary{normalize(page.ID), page.Title(), page.URL}
}

func (s summary) dict() map[string]any {
	return map[string]any{"page_id": s.pageID, "title": s.title, "url": s.url}
}

func parentID(page notion.RemotePage) string {
	kind, id := page.Parent.Ref()
	if kind == "page_id" {
		return id
	}
	return ""
}

func hubOf(ctx context.Context, tc *tool.Context, root, value string, defaultRoot bool) (string, error) {
	if value == "" {
		if defaultRoot {
			return root, nil
		}
		return "", tool.Error("parent_page_id is required")
	}
	parent := normalize(value)
	in, err := notion.InTree(ctx, tc.DB.Read, parent, root)
	if err != nil {
		return "", err
	}
	if !in {
		return "", tool.Error("parent_page_id is not a page of the wiki tree (Wiki Home or a page under it)")
	}
	return parent, nil
}

func cleanArgs(wiki notion.Wiki, args tool.Args) (aliases, sources []string, err error) {
	raw := dictGet(args, "aliases")
	if !truthy(raw) {
		raw = []any{}
	}
	if aliases, err = wiki.CleanAliases(raw); err == nil {
		sources, err = wiki.CleanSources(dictGet(args, "sources"))
	}
	var failure notion.WikiError
	if errors.As(err, &failure) {
		return nil, nil, tool.Error(string(failure))
	}
	return aliases, sources, err
}

func today(tc *tool.Context) (string, error) { return notion.LocalDate(tc.Now(), tc.Env) }

func editedText(v any) string {
	s, _ := v.(string)
	return s
}

func optionalText(args tool.Args, name string) (string, error) {
	text, _, err := tool.TextArg(args, name, false)
	return text, err
}

type write struct {
	id           int64
	tool, target string
	before       any
	after        any
	undo         any
}

func beginWrite(ctx context.Context, tc *tool.Context, tool, target string) (int64, error) {
	var thread, run *int64
	if tc.ThreadID != 0 {
		thread = &tc.ThreadID
	}
	if tc.RunID != 0 {
		run = &tc.RunID
	}
	return writes.Begin(ctx, tc.DB.Write, writes.New{TS: tc.Now(), ThreadID: thread, RunID: run, Tool: tool, Target: target})
}

func (w write) commit(ctx context.Context, tc *tool.Context, upsert func(*sql.Tx) error) (int64, error) {
	err := tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if err := upsert(tx); err != nil {
			return err
		}
		return writes.Settle(ctx, tx, tc.Undoers, w.id, writes.New{
			Target: w.target, Before: w.before, After: w.after, Undo: w.undo,
		})
	})
	return w.id, err
}

func (w write) finish(ctx context.Context, tc *tool.Context, client *notion.Client, wiki notion.Wiki, upsert func(*sql.Tx) error, action string, s summary) (int64, error) {
	id, err := w.commit(ctx, tc, upsert)
	if err != nil {
		return 0, err
	}
	date, err := today(tc)
	if err != nil {
		return 0, err
	}
	logged := func() error {
		writer := &notion.Writer{DB: tc.DB, Client: client}
		return writer.Capture(ctx, []string{notion.LogLine(date, action, s.title, s.url)})
	}
	if err := logged(); err != nil {
		var failure notion.WikiError
		if errors.As(err, &failure) || notionOnly(err) {
			return 0, tool.Error("the page was written (write_id " + strconv.FormatInt(id, 10) + ") but the Wiki Log could not be updated: " + err.Error())
		}
		return 0, err
	}
	return id, nil
}

func notionOnly(err error) bool {
	return notionError(err) != err
}

func editedTime(ctx context.Context, client *notion.Client, pageID string) (string, error) {
	page, err := client.GetPage(ctx, pageID)
	if err != nil {
		return "", err
	}
	return page.LastEditedTime, nil
}

func strs(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func createPage(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	title, err := required(args, "title")
	if err != nil {
		return nil, err
	}
	markdown := ""
	if v := dictGet(args, "markdown"); truthy(v) {
		s, ok := v.(string)
		if !ok {
			return nil, tool.Error("markdown must be a string")
		}
		markdown = s
	}
	allowDuplicate := false
	if v, present := args.Get("allow_duplicate"); present {
		b, ok := v.(bool)
		if !ok {
			return nil, tool.Error("allow_duplicate must be true or false")
		}
		allowDuplicate = b
	}
	if err := checkMarkdown(ctx, tc, markdown); err != nil {
		return nil, err
	}
	wiki, err := wikiOf(ctx, tc)
	if err != nil {
		return nil, err
	}
	hubArg, err := optionalText(args, "parent_page_id")
	if err != nil {
		return nil, err
	}
	parent, err := hubOf(ctx, tc, wiki.Root, hubArg, true)
	if err != nil {
		return nil, err
	}
	aliases, sources, err := cleanArgs(wiki, args)
	if err != nil {
		return nil, err
	}
	if !allowDuplicate {
		hits, err := notion.FindExisting(ctx, tc.DB.Read, title, aliases)
		if err != nil {
			return nil, err
		}
		if len(hits) > 0 {
			parts := make([]string, 0, 5)
			for _, h := range hits[:min(5, len(hits))] {
				parts = append(parts, h.ID+" ("+strconv.Quote(h.Title)+", "+h.Match+")")
			}
			return nil, tool.Error("wiki already has matching page(s): " + strings.Join(parts, ", ") +
				". Update that page, or pass allow_duplicate=true if this is genuinely a different entity.")
		}
	}
	date, err := today(tc)
	if err != nil {
		return nil, err
	}
	body, merged, err := wiki.WithFooter(markdown, date, sources)
	if err != nil {
		return nil, err
	}
	client, err := makeClient(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	id, err := beginWrite(ctx, tc, "notion_create_page", title)
	if err != nil {
		return nil, err
	}
	writer := &notion.Writer{DB: tc.DB, Client: client}
	page, err := writer.Create(ctx, parent, title, body)
	if err != nil {
		_ = writes.Discard(ctx, tc.DB.Write, id)
		return nil, notionError(err)
	}
	s := summarize(page)
	edited, err := editedTime(ctx, client, s.pageID)
	if err != nil {
		return nil, notionError(err)
	}
	after := s.dict()
	after["parent_page_id"] = parent
	after["markdown"] = body
	after["aliases"] = aliases
	w := write{
		id: id, tool: "notion_create_page", target: s.pageID,
		after: after,
		undo:  pageUndo{Op: undoTrash, PageID: s.pageID, AppliedEditedTime: editedText(edited)},
	}
	id, err = w.finish(ctx, tc, client, wiki, func(tx *sql.Tx) error {
		return notion.UpsertWiki(ctx, tx, s.pageID, tc.Now(), notion.WikiFields{
			Title: new(title), HubID: new(parent), Aliases: aliases, SetAliases: true, LastReviewed: new(date),
			Sources: merged, SetSources: true, InTrash: new(false),
		})
	}, "created", s)
	if err != nil {
		return nil, err
	}
	out := s.dict()
	out["parent_page_id"] = parent
	out["aliases"] = aliases
	out["write_id"] = id
	return out, nil
}

func newMarkdown(previous string, replace, appendText *string, section string) (string, error) {
	if section != "" {
		if replace != nil {
			updated, ok, err := notion.ReplaceSection(previous, section, *replace)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", tool.Error("page has no section titled " + strconv.Quote(section) + "; use append_markdown to add one")
			}
			return updated, nil
		}
		return notion.AppendToSection(previous, section, *appendText)
	}
	if replace != nil {
		return *replace, nil
	}
	return strings.TrimRight(previous, "\n") + "\n\n" + *appendText, nil
}

func optionalString(args tool.Args, name string) (*string, error) {
	v := dictGet(args, name)
	if v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, tool.Error(name + " must be a string")
	}
	return &s, nil
}

func updatePage(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw, err := required(args, "page_id")
	if err != nil {
		return nil, err
	}
	pageID := normalize(raw)
	replace, err := optionalString(args, "markdown_replace")
	if err != nil {
		return nil, err
	}
	appendText, err := optionalString(args, "append_markdown")
	if err != nil {
		return nil, err
	}
	section, err := optionalText(args, "section")
	if err != nil {
		return nil, err
	}
	switch {
	case replace != nil && appendText != nil:
		return nil, tool.Error("give markdown_replace or append_markdown, not both")
	case replace == nil && appendText == nil:
		return nil, tool.Error("nothing to change")
	}
	checked := replace
	if checked == nil {
		checked = appendText
	}
	if err := checkMarkdown(ctx, tc, *checked); err != nil {
		return nil, err
	}
	wiki, err := wikiOf(ctx, tc)
	if err != nil {
		return nil, err
	}
	_, sources, err := cleanArgs(wiki, args)
	if err != nil {
		return nil, err
	}
	date, err := today(tc)
	if err != nil {
		return nil, err
	}
	client, err := makeClient(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	id, err := beginWrite(ctx, tc, "notion_update_page", pageID)
	if err != nil {
		return nil, err
	}
	page, previous, updated, merged, edited, err := rewrite(ctx, tc.DB, wiki, client, pageID, replace, appendText, section, date, sources)
	if err != nil {
		var te tool.Error
		if errors.As(err, &te) {
			_ = writes.Discard(ctx, tc.DB.Write, id)
		}
		return nil, notionError(err)
	}
	s := summarize(page)
	w := write{
		id: id, tool: "notion_update_page", target: pageID,
		before: map[string]any{"markdown": previous}, after: map[string]any{"markdown": updated},
		undo: restoreUndo{pageUndo: pageUndo{Op: undoRestore, PageID: pageID, AppliedEditedTime: editedText(edited)}, PreviousMarkdown: previous},
	}
	id, err = w.finish(ctx, tc, client, wiki, func(tx *sql.Tx) error {
		return notion.UpsertWiki(ctx, tx, pageID, tc.Now(), notion.WikiFields{
			Title: new(s.title), HubID: new(parentID(page)), LastReviewed: new(date), Sources: merged, SetSources: true, InTrash: new(false),
		})
	}, "updated", s)
	if err != nil {
		return nil, err
	}
	out := s.dict()
	out["write_id"] = id
	out["changed"] = []any{"markdown"}
	return out, nil
}

func rewrite(ctx context.Context, d *db.DB, wiki notion.Wiki, client *notion.Client, pageID string, replace, appendText *string, section, date string, sources []string) (
	page notion.RemotePage, previous, updated string, merged []string, edited string, err error) {
	writer := &notion.Writer{DB: d, Client: client}
	return writer.Rewrite(ctx, pageID, replace, appendText, section, date, sources)
}

func replaceContent(text string) map[string]any {
	return map[string]any{"type": "replace_content", "replace_content": map[string]any{"new_str": text}}
}

func movePage(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw, err := required(args, "page_id")
	if err != nil {
		return nil, err
	}
	pageID := normalize(raw)
	wiki, err := wikiOf(ctx, tc)
	if err != nil {
		return nil, err
	}
	if pageID == wiki.Root {
		return nil, tool.Error("Wiki Home itself cannot be moved")
	}
	parentArg, err := required(args, "parent_page_id")
	if err != nil {
		return nil, err
	}
	parent, err := hubOf(ctx, tc, wiki.Root, parentArg, false)
	if err != nil {
		return nil, err
	}
	under, err := notion.InTree(ctx, tc.DB.Read, parent, pageID)
	if err != nil {
		return nil, err
	}
	if parent == pageID || under {
		return nil, tool.Error("a page cannot be moved under itself")
	}
	client, err := makeClient(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	id, err := beginWrite(ctx, tc, "notion_move_page", pageID)
	if err != nil {
		return nil, err
	}
	page, previous, edited, err := relocate(ctx, tc.DB, client, pageID, parent)
	if err != nil {
		var te tool.Error
		if errors.As(err, &te) {
			_ = writes.Discard(ctx, tc.DB.Write, id)
		}
		return nil, notionError(err)
	}
	s := summarize(page)
	w := write{
		id: id, tool: "notion_move_page", target: pageID,
		before: map[string]any{"parent_page_id": previous}, after: map[string]any{"parent_page_id": parent},
		undo: moveUndo{pageUndo: pageUndo{Op: undoMove, PageID: pageID, AppliedEditedTime: editedText(edited)}, PreviousParentID: previous},
	}
	id, err = w.finish(ctx, tc, client, wiki, func(tx *sql.Tx) error {
		return notion.UpsertWiki(ctx, tx, pageID, tc.Now(), notion.WikiFields{Title: new(s.title), HubID: new(parent), InTrash: new(false)})
	}, "moved", s)
	if err != nil {
		return nil, err
	}
	out := s.dict()
	out["parent_page_id"] = parent
	out["write_id"] = id
	return out, nil
}

func relocate(ctx context.Context, d *db.DB, client *notion.Client, pageID, parent string) (page notion.RemotePage, previous, edited string, err error) {
	writer := &notion.Writer{DB: d, Client: client}
	return writer.Move(ctx, pageID, parent)
}

func archivePage(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw, err := required(args, "page_id")
	if err != nil {
		return nil, err
	}
	pageID := normalize(raw)
	wiki, err := wikiOf(ctx, tc)
	if err != nil {
		return nil, err
	}
	if pageID == wiki.Root {
		return nil, tool.Error("Wiki Home itself cannot be archived")
	}
	client, err := makeClient(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	id, err := beginWrite(ctx, tc, "notion_archive_page", pageID)
	if err != nil {
		return nil, err
	}
	page, previous, edited, err := trash(ctx, tc.DB, client, pageID)
	if err != nil {
		return nil, notionError(err)
	}
	s := summarize(page)
	w := write{
		id: id, tool: "notion_archive_page", target: pageID,
		before: map[string]any{"title": s.title, "markdown": previous.Markdown}, after: map[string]any{"in_trash": true},
		undo: pageUndo{Op: undoUntrash, PageID: pageID, AppliedEditedTime: editedText(edited)},
	}
	id, err = w.finish(ctx, tc, client, wiki, func(tx *sql.Tx) error {
		return notion.UpsertWiki(ctx, tx, pageID, tc.Now(), notion.WikiFields{Title: new(s.title), InTrash: new(true)})
	}, "archived", s)
	if err != nil {
		return nil, err
	}
	out := s.dict()
	out["archived"] = true
	out["write_id"] = id
	return out, nil
}

func trash(ctx context.Context, d *db.DB, client *notion.Client, pageID string) (page notion.RemotePage, previous notion.Markdown, edited string, err error) {
	writer := &notion.Writer{DB: d, Client: client}
	return writer.Archive(ctx, pageID)
}

func addAlias(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw, err := required(args, "page_id")
	if err != nil {
		return nil, err
	}
	pageID := normalize(raw)
	wiki, err := wikiOf(ctx, tc)
	if err != nil {
		return nil, err
	}
	added, err := wiki.CleanAliases(dictGet(args, "aliases"))
	if err != nil {
		var failure notion.WikiError
		if errors.As(err, &failure) {
			return nil, tool.Error(string(failure))
		}
		return nil, err
	}
	if len(added) == 0 {
		return nil, tool.Error("aliases must name at least one alias")
	}
	hits, err := notion.FindExisting(ctx, tc.DB.Read, "", added)
	if err != nil {
		return nil, err
	}
	var taken []string
	for _, h := range hits {
		if h.ID != pageID && len(taken) < 5 {
			taken = append(taken, h.ID+" ("+strconv.Quote(h.Title)+")")
		}
	}
	if len(taken) > 0 {
		return nil, tool.Error("alias already names another page: " + strings.Join(taken, ", "))
	}
	client, err := makeClient(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	page, err := livePageOf(ctx, tc.DB.Read, client, pageID)
	if err != nil {
		return nil, notionError(err)
	}
	s := summarize(page)
	local, err := notion.GetWiki(ctx, tc.DB.Read, pageID)
	if err != nil {
		return nil, err
	}
	previous := []string{}
	if local != nil {
		previous = local.Aliases
	}
	aliases, err := wiki.CleanAliases(strs(append(append([]string{}, previous...), added...)))
	if err != nil {
		return nil, err
	}
	id, err := beginWrite(ctx, tc, "notion_add_alias", pageID)
	if err != nil {
		return nil, err
	}
	w := write{
		id: id, tool: "notion_add_alias", target: pageID,
		before: map[string]any{"aliases": previous}, after: map[string]any{"aliases": aliases},
		undo: aliasesUndo{Op: undoAliases, PageID: pageID, PreviousAliases: previous},
	}
	id, err = w.finish(ctx, tc, client, wiki, func(tx *sql.Tx) error {
		return notion.UpsertWiki(ctx, tx, pageID, tc.Now(), notion.WikiFields{
			Title: new(s.title), HubID: new(parentID(page)), Aliases: aliases, SetAliases: true, InTrash: new(false),
		})
	}, "aliased "+strings.Join(added, ", ")+" →", s)
	if err != nil {
		return nil, err
	}
	out := s.dict()
	out["aliases"] = aliases
	out["write_id"] = id
	return out, nil
}

const (
	markdownNote = "Notion-flavoured Markdown. External image or embed URLs are rejected."
	wikiNote     = " The write is logged on the Wiki Log page, the page footer 'Last reviewed <date> · sources' is refreshed, and the operator can undo it."
	aliasesProp  = `{"type": "array", "items": {"type": "string"}, "description": "Other names the subject goes by; used to find the page again."}`
	sourcesProp  = `{"type": "array", "items": {"type": "string"}, "description": "Where the facts come from: URLs, or short labels like 'mail #123' or 'chat 2026-10-17'."}`
)

func schema(properties string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	return json.RawMessage(fmt.Sprintf(`{"type": "object", "properties": %s, "required": %s, "additionalProperties": false}`, properties, req))
}

func writeTools() []tool.Tool {
	return []tool.Tool{
		{
			Name:        "notion_create_page",
			Description: "Create a wiki page under a hub page (parent_page_id; Wiki Home when omitted). Search first (search_knowledge): a page whose title or alias already matches is refused unless allow_duplicate is true. " + markdownNote + wikiNote,
			InputSchema: schema(`{"parent_page_id": {"type": "string"}, "title": {"type": "string"}, "markdown": {"type": "string"}, "aliases": `+aliasesProp+`, "sources": `+sourcesProp+`, "allow_duplicate": {"type": "boolean"}}`, "title", "markdown"),
			Handler:     createPage, Effect: tool.Write, Contexts: writeKinds,
		},
		{
			Name:        "notion_update_page",
			Description: "Change a wiki page: replace its body (markdown_replace) or append to it (append_markdown); with section (a heading text) only that section is replaced or appended to, and a missing section is added by append. Fetch the page first. " + markdownNote + wikiNote,
			InputSchema: schema(`{"page_id": {"type": "string"}, "markdown_replace": {"type": "string"}, "append_markdown": {"type": "string"}, "section": {"type": "string"}, "sources": `+sourcesProp+`}`, "page_id"),
			Handler:     updatePage, Effect: tool.Write, Contexts: writeKinds,
		},
		{
			Name:        "notion_move_page",
			Description: "Move a wiki page under another hub page." + wikiNote,
			InputSchema: schema(`{"page_id": {"type": "string"}, "parent_page_id": {"type": "string"}}`, "page_id", "parent_page_id"),
			Handler:     movePage, Effect: tool.Write, Contexts: writeKinds,
		},
		{
			Name:        "notion_archive_page",
			Description: "Move a wiki page to the trash (never deleted)." + wikiNote,
			InputSchema: schema(`{"page_id": {"type": "string"}}`, "page_id"),
			Handler:     archivePage, Effect: tool.Write, Contexts: writeKinds,
		},
		{
			Name:        "notion_add_alias",
			Description: "Record other names for a wiki page so search_knowledge and the duplicate check find it; an alias in use by another page is refused." + wikiNote,
			InputSchema: schema(`{"page_id": {"type": "string"}, "aliases": `+aliasesProp+`}`, "page_id", "aliases"),
			Handler:     addAlias, Effect: tool.Write, Contexts: writeKinds,
		},
	}
}

func Tools() []tool.Tool { return append(readTools(), writeTools()...) }

func CreatePage(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	return createPage(ctx, tc, args)
}
