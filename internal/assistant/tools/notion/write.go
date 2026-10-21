package notiontools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/py"
)

const (
	maxMarkdownChars = 100_000
	undoRestore      = "notion_page_restore"
	undoTrash        = "notion_page_trash"
	undoUntrash      = "notion_page_untrash"
	undoMove         = "notion_page_move"
	undoAliases      = "notion_aliases_restore"
)

var writeKinds = tool.Kinds{tool.Chat, tool.Nightly}

func normalize(id string) string { return notionid.Normalize(id) }

func checkMarkdown(tc *tool.Context, markdown string) error {
	if len([]rune(markdown)) > maxMarkdownChars {
		return tool.Error("markdown is limited to " + strconv.Itoa(maxMarkdownChars) + " characters")
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

func livePageOf(ctx context.Context, q db.Querier, client *notion.Client, pageID string) (py.Dict, error) {
	page, err := client.GetPage(ctx, pageID)
	if err != nil {
		return nil, err
	}
	if py.Truthy(dictGet(page, "in_trash")) {
		return nil, tool.Error("page is in the trash")
	}
	properties, _ := dictGet(page, "properties").(py.Dict)
	sensitivity, err := notion.LoadSensitivity(ctx, q)
	if err != nil {
		return nil, err
	}
	if sensitivity.IsPrivate(sensitivity.Of(properties)) {
		return nil, tool.Error("page is private")
	}
	return page, nil
}

func richText(text string) []any {
	return []any{py.Dict{{Key: "type", Value: "text"}, {Key: "text", Value: py.Dict{{Key: "content", Value: text}}}}}
}

type summary struct{ pageID, title, url string }

func summarize(page py.Dict) summary {
	return summary{normalize(str(page, "id")), notion.PageTitle(page), str(page, "url")}
}

func (s summary) dict() py.Dict {
	return py.Dict{{Key: "page_id", Value: s.pageID}, {Key: "title", Value: s.title}, {Key: "url", Value: s.url}}
}

func parentID(page py.Dict) string {
	parent, _ := dictGet(page, "parent").(py.Dict)
	kind, id := notion.ParentRef(parent)
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

func cleanArgs(wiki notion.Wiki, args py.Dict) (aliases, sources []string, err error) {
	raw := dictGet(args, "aliases")
	if !py.Truthy(raw) {
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

func optionalText(args py.Dict, name string) (string, error) {
	text, _, err := tool.TextArg(args, name, false)
	return text, err
}

type write struct {
	tool, target string
	before       any
	after        any
	undo         any
}

func (w write) commit(ctx context.Context, tc *tool.Context, upsert func(*sql.Tx) error) (int64, error) {
	var id int64
	err := tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if err := upsert(tx); err != nil {
			return err
		}
		var err error
		var thread, run *int64
		if tc.ThreadID != 0 {
			thread = &tc.ThreadID
		}
		if tc.RunID != 0 {
			run = &tc.RunID
		}
		id, err = writes.Record(ctx, tx, writes.New{
			TS: tc.Now(), ThreadID: thread, RunID: run, Tool: w.tool, Target: w.target,
			Before: w.before, After: w.after, Undo: w.undo,
		})
		return err
	})
	return id, err
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
		logID, err := wiki.LogPageID(ctx, tc.DB, client, wiki.Root)
		if err != nil {
			return err
		}
		_, err = notion.PrependLog(ctx, client, logID, []string{notion.LogLine(date, action, s.title, s.url)})
		return err
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

func editedTime(ctx context.Context, client *notion.Client, pageID string) (any, error) {
	page, err := client.GetPage(ctx, pageID)
	if err != nil {
		return nil, err
	}
	return dictGet(page, "last_edited_time"), nil
}

func strs(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func createPage(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	title, err := required(args, "title")
	if err != nil {
		return nil, err
	}
	markdown := ""
	if v := dictGet(args, "markdown"); py.Truthy(v) {
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
	if err := checkMarkdown(tc, markdown); err != nil {
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
				parts = append(parts, h.ID+" ("+py.Repr(h.Title)+", "+h.Match+")")
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
	client, err := makeClient(ctx, tc.DB.Read, tc.Env)
	if err != nil {
		return nil, err
	}
	page, err := client.CreatePage(ctx, py.Dict{
		{Key: "parent", Value: py.Dict{{Key: "page_id", Value: parent}}},
		{Key: "properties", Value: py.Dict{{Key: "title", Value: py.Dict{{Key: "title", Value: richText(title)}}}}},
		{Key: "markdown", Value: body},
	})
	var s summary
	var edited any
	if err == nil {
		s = summarize(page)
		edited, err = editedTime(ctx, client, s.pageID)
	}
	if err != nil {
		return nil, notionError(err)
	}
	w := write{
		tool: "notion_create_page", target: s.pageID,
		after: append(s.dict(), py.Pair{Key: "parent_page_id", Value: parent}, py.Pair{Key: "markdown", Value: body}, py.Pair{Key: "aliases", Value: aliases}),
		undo:  py.Dict{{Key: "op", Value: undoTrash}, {Key: "page_id", Value: s.pageID}, {Key: "applied_edited_time", Value: edited}},
	}
	id, err := w.finish(ctx, tc, client, wiki, func(tx *sql.Tx) error {
		return notion.UpsertWiki(ctx, tx, s.pageID, tc.Now(), notion.WikiFields{
			Title: new(title), HubID: new(parent), Aliases: aliases, SetAliases: true, LastReviewed: new(date),
			Sources: merged, SetSources: true, InTrash: new(false),
		})
	}, "created", s)
	if err != nil {
		return nil, err
	}
	return append(s.dict(), py.Pair{Key: "parent_page_id", Value: parent}, py.Pair{Key: "aliases", Value: aliases}, py.Pair{Key: "write_id", Value: id}), nil
}

func newMarkdown(previous string, replace, appendText *string, section string) (string, error) {
	if section != "" {
		if replace != nil {
			updated, ok, err := notion.ReplaceSection(previous, section, *replace)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", tool.Error("page has no section titled " + py.Repr(section) + "; use append_markdown to add one")
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

func optionalString(args py.Dict, name string) (*string, error) {
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

func updatePage(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
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
	if err := checkMarkdown(tc, *checked); err != nil {
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
	client, err := makeClient(ctx, tc.DB.Read, tc.Env)
	if err != nil {
		return nil, err
	}
	page, previous, updated, merged, edited, err := rewrite(ctx, tc.DB.Read, wiki, client, pageID, replace, appendText, section, date, sources)
	if err != nil {
		return nil, notionError(err)
	}
	s := summarize(page)
	w := write{
		tool: "notion_update_page", target: pageID,
		before: py.Dict{{Key: "markdown", Value: previous}}, after: py.Dict{{Key: "markdown", Value: updated}},
		undo: py.Dict{{Key: "op", Value: undoRestore}, {Key: "page_id", Value: pageID}, {Key: "previous_markdown", Value: previous}, {Key: "applied_edited_time", Value: edited}},
	}
	id, err := w.finish(ctx, tc, client, wiki, func(tx *sql.Tx) error {
		return notion.UpsertWiki(ctx, tx, pageID, tc.Now(), notion.WikiFields{
			Title: new(s.title), HubID: new(parentID(page)), LastReviewed: new(date), Sources: merged, SetSources: true, InTrash: new(false),
		})
	}, "updated", s)
	if err != nil {
		return nil, err
	}
	return append(s.dict(), py.Pair{Key: "write_id", Value: id}, py.Pair{Key: "changed", Value: []any{"markdown"}}), nil
}

func rewrite(ctx context.Context, q db.Querier, wiki notion.Wiki, client *notion.Client, pageID string, replace, appendText *string, section, date string, sources []string) (
	page py.Dict, previous, updated string, merged []string, edited any, err error) {
	if page, err = livePageOf(ctx, q, client, pageID); err != nil {
		return
	}
	current, err := client.PageMarkdown(ctx, pageID)
	if err != nil {
		return
	}
	if py.Truthy(dictGet(current, "truncated")) {
		err = tool.Error("page is too large to rewrite safely")
		return
	}
	previous = str(current, "markdown")
	body, _, prior, err := notion.SplitFooter(previous)
	if err != nil {
		return
	}
	next, err := newMarkdown(body, replace, appendText, section)
	if err != nil {
		return
	}
	if updated, merged, err = wiki.WithFooter(next, date, append(append([]string{}, prior...), sources...)); err != nil {
		return
	}
	if len([]rune(updated)) > maxMarkdownChars {
		err = tool.Error("page would exceed " + strconv.Itoa(maxMarkdownChars) + " characters")
		return
	}
	if _, err = client.UpdateMarkdown(ctx, pageID, replaceContent(updated)); err != nil {
		return
	}
	edited, err = editedTime(ctx, client, pageID)
	return
}

func replaceContent(text string) py.Dict {
	return py.Dict{{Key: "type", Value: "replace_content"}, {Key: "replace_content", Value: py.Dict{{Key: "new_str", Value: text}}}}
}

func movePage(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
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
	client, err := makeClient(ctx, tc.DB.Read, tc.Env)
	if err != nil {
		return nil, err
	}
	page, previous, edited, err := relocate(ctx, tc.DB.Read, client, pageID, parent)
	if err != nil {
		return nil, notionError(err)
	}
	s := summarize(page)
	w := write{
		tool: "notion_move_page", target: pageID,
		before: py.Dict{{Key: "parent_page_id", Value: previous}}, after: py.Dict{{Key: "parent_page_id", Value: parent}},
		undo: py.Dict{{Key: "op", Value: undoMove}, {Key: "page_id", Value: pageID}, {Key: "previous_parent_id", Value: previous}, {Key: "applied_edited_time", Value: edited}},
	}
	id, err := w.finish(ctx, tc, client, wiki, func(tx *sql.Tx) error {
		return notion.UpsertWiki(ctx, tx, pageID, tc.Now(), notion.WikiFields{Title: new(s.title), HubID: new(parent), InTrash: new(false)})
	}, "moved", s)
	if err != nil {
		return nil, err
	}
	return append(s.dict(), py.Pair{Key: "parent_page_id", Value: parent}, py.Pair{Key: "write_id", Value: id}), nil
}

func relocate(ctx context.Context, q db.Querier, client *notion.Client, pageID, parent string) (page py.Dict, previous string, edited any, err error) {
	if page, err = livePageOf(ctx, q, client, pageID); err != nil {
		return
	}
	if previous = parentID(page); previous == "" {
		err = tool.Error("only pages whose parent is a page can be moved; database rows stay where they are")
		return
	}
	if previous == parent {
		err = tool.Error("the page is already under that parent")
		return
	}
	if _, err = client.MovePage(ctx, pageID, py.Dict{{Key: "type", Value: "page_id"}, {Key: "page_id", Value: parent}}); err != nil {
		return
	}
	edited, err = editedTime(ctx, client, pageID)
	return
}

func archivePage(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
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
	client, err := makeClient(ctx, tc.DB.Read, tc.Env)
	if err != nil {
		return nil, err
	}
	page, previous, edited, err := trash(ctx, tc.DB.Read, client, pageID)
	if err != nil {
		return nil, notionError(err)
	}
	s := summarize(page)
	w := write{
		tool: "notion_archive_page", target: pageID,
		before: py.Dict{{Key: "title", Value: s.title}, {Key: "markdown", Value: str(previous, "markdown")}}, after: py.Dict{{Key: "in_trash", Value: true}},
		undo: py.Dict{{Key: "op", Value: undoUntrash}, {Key: "page_id", Value: pageID}, {Key: "applied_edited_time", Value: edited}},
	}
	id, err := w.finish(ctx, tc, client, wiki, func(tx *sql.Tx) error {
		return notion.UpsertWiki(ctx, tx, pageID, tc.Now(), notion.WikiFields{Title: new(s.title), InTrash: new(true)})
	}, "archived", s)
	if err != nil {
		return nil, err
	}
	return append(s.dict(), py.Pair{Key: "archived", Value: true}, py.Pair{Key: "write_id", Value: id}), nil
}

func trash(ctx context.Context, q db.Querier, client *notion.Client, pageID string) (page, previous py.Dict, edited any, err error) {
	if page, err = livePageOf(ctx, q, client, pageID); err != nil {
		return
	}
	if previous, err = client.PageMarkdown(ctx, pageID); err != nil {
		return
	}
	if _, err = client.SetTrash(ctx, pageID, true); err != nil {
		return
	}
	edited, err = editedTime(ctx, client, pageID)
	return
}

func addAlias(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
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
			taken = append(taken, h.ID+" ("+py.Repr(h.Title)+")")
		}
	}
	if len(taken) > 0 {
		return nil, tool.Error("alias already names another page: " + strings.Join(taken, ", "))
	}
	client, err := makeClient(ctx, tc.DB.Read, tc.Env)
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
	w := write{
		tool: "notion_add_alias", target: pageID,
		before: py.Dict{{Key: "aliases", Value: previous}}, after: py.Dict{{Key: "aliases", Value: aliases}},
		undo: py.Dict{{Key: "op", Value: undoAliases}, {Key: "page_id", Value: pageID}, {Key: "previous_aliases", Value: previous}},
	}
	id, err := w.finish(ctx, tc, client, wiki, func(tx *sql.Tx) error {
		return notion.UpsertWiki(ctx, tx, pageID, tc.Now(), notion.WikiFields{
			Title: new(s.title), HubID: new(parentID(page)), Aliases: aliases, SetAliases: true, InTrash: new(false),
		})
	}, "aliased "+strings.Join(added, ", ")+" →", s)
	if err != nil {
		return nil, err
	}
	return append(s.dict(), py.Pair{Key: "aliases", Value: aliases}, py.Pair{Key: "write_id", Value: id}), nil
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

func CreatePage(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	return createPage(ctx, tc, args)
}
