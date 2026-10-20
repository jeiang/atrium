package notion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	KindLink       = "notion_link"
	KindEdit       = "notion_edit"
	UndoRestoreOp  = "notion_restore"
	relatedHeading = "Related"
	linkPageChars  = 2500
	editPageChars  = 12000
	linkMaxTokens  = 160
	editMaxTokens  = 1500
	diffContext    = 2
	linkSystem     = "You maintain cross-links between pages of the owner's personal notes workspace. You get page A and page B (title, location, opening text). Answer link=true only when the pages are clearly about the same subject, project, person or task so that a reader of A would want to open B. Similar vocabulary alone is not enough. reason is one short sentence naming what connects them."
	editSystem     = "You draft a small, targeted edit to one page of the owner's notes, written in Markdown. You get the page Markdown and an instruction. Return the minimal list of replacements: each old_str must be copied verbatim from the page and occur exactly once in it; new_str is its replacement. Change only what the instruction asks for. summary is one short sentence describing the edit. If the instruction cannot be applied as a replacement of existing text, return an empty updates list."
	linkSchemaJSON = `{"type": "object", "properties": {"link": {"type": "boolean"}, "reason": {"type": "string"}}, "required": ["link", "reason"], "additionalProperties": false}`
	editSchemaJSON = `{"type": "object", "properties": {"updates": {"type": "array", "items": {"type": "object", "properties": {"old_str": {"type": "string"}, "new_str": {"type": "string"}}, "required": ["old_str", "new_str"], "additionalProperties": false}}, "summary": {"type": "string"}}, "required": ["updates", "summary"], "additionalProperties": false}`
)

var (
	propHeadRe = rx.MustCompile(`^#{1,6}\s+(.*?)\s*#*\s*$`, 0)
	listItemRe = rx.MustCompile(`^\s*(?:[-*+]|\d+\.)\s`, 0)
)

type ProposalError string

func (e ProposalError) Error() string { return string(e) }

func (ProposalError) PyClass() string { return "ProposalError" }

func (ProposalError) PyModule() string { return "atrium.notion_proposals" }

type StaleError string

func (e StaleError) Error() string { return string(e) }

func (StaleError) PyClass() string { return "StaleError" }

func (StaleError) PyModule() string { return "atrium.notion_proposals" }

type ChatFunc func(ctx context.Context, url, system, user string, schema any, maxTokens int) (json.RawMessage, error)

func (p *Page) Ref() PageRef { return pageRef(p.ID, p.Title, p.URL, p.Path, p.Private != 0) }

func (r PageRef) Dict() py.Dict {
	path := make([]any, len(r.Path))
	for i, part := range r.Path {
		var id any
		if part.ID != nil {
			id = *part.ID
		}
		path[i] = py.Dict{{Key: "id", Value: id}, {Key: "title", Value: part.Title}}
	}
	return py.Dict{{Key: "id", Value: r.ID}, {Key: "title", Value: r.Title}, {Key: "url", Value: r.URL}, {Key: "path", Value: path}, {Key: "private", Value: r.Private}}
}

type Proposer struct {
	DB   *db.DB
	Cfg  config.Config
	Chat ChatFunc
	Now  func() float64
}

func (p *Proposer) now() float64 {
	if p.Now != nil {
		return p.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (p *Proposer) chat(ctx context.Context, system, user, schema string, maxTokens int) (py.Dict, error) {
	call := p.Chat
	if call == nil {
		call = llm.ChatJSON
	}
	raw, err := call(ctx, p.Cfg.ChatURL, system, user, json.RawMessage(schema), maxTokens)
	if err != nil {
		return nil, err
	}
	v, err := py.JSONLoads(raw)
	if err != nil {
		return nil, err
	}
	d, ok := v.(py.Dict)
	if !ok {
		return nil, py.AttributeError(v, "get")
	}
	return d, nil
}

func fingerprint(parts ...any) string {
	sum := sha256.Sum256([]byte(py.JSONDumps(parts)))
	return hex.EncodeToString(sum[:])[:32]
}

func (p *Proposer) load(ctx context.Context, id string) (*Page, error) {
	page, err := GetPage(ctx, p.DB.Read, id)
	if err != nil {
		return nil, err
	}
	if page == nil || page.InTrash != 0 {
		return nil, ProposalError("unknown page " + id)
	}
	return page, nil
}

func orUntitledTitle(title string) string {
	if title == "" {
		return "Untitled"
	}
	return title
}

func (p *Proposer) create(ctx context.Context, kind, title string, payload, preview py.Dict, fp string) (*int64, error) {
	account := Account
	id, ok, err := proposals.Create(ctx, p.DB, kind, &account, title, payload, preview, fp, p.now())
	if err != nil || !ok {
		return nil, err
	}
	return &id, nil
}

func (p *Proposer) ProposeLink(ctx context.Context, sourceID, targetID string) (*int64, error) {
	source, err := p.load(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	target, err := p.load(ctx, targetID)
	if err != nil {
		return nil, err
	}
	if source.ID == target.ID {
		return nil, ProposalError("cannot link a page to itself")
	}
	if (source.Private != 0) != (target.Private != 0) {
		return nil, ProposalError("private and non-private pages are never compared")
	}
	links, err := py.JSONLoads([]byte(source.Links))
	if err != nil {
		return nil, err
	}
	if items, _ := links.([]any); slices.Contains(items, any(target.ID)) {
		return nil, nil
	}
	block := func(label string, page *Page) string {
		return label + ":\nTitle: " + page.Title + "\nLocation: " + page.Path + "\nText:\n" + py.Clip(page.Markdown, linkPageChars)
	}
	answer, err := p.chat(ctx, linkSystem, block("Page A", source)+"\n\n"+block("Page B", target), linkSchemaJSON, linkMaxTokens)
	if err != nil {
		return nil, err
	}
	if v, _ := answer.Get("link"); !py.Truthy(v) {
		return nil, nil
	}
	reason := ""
	if v, ok := answer.Get("reason"); ok {
		reason = fmt.Sprint(v)
		if s, isString := v.(string); isString {
			reason = s
		}
	}
	payload := py.Dict{
		{Key: "source_id", Value: source.ID}, {Key: "target_id", Value: target.ID},
		{Key: "source_title", Value: source.Title}, {Key: "target_title", Value: target.Title},
		{Key: "target_url", Value: target.URL}, {Key: "reason", Value: reason}, {Key: "snapshot", Value: source.LastEditedTime},
	}
	preview := py.Dict{{Key: "a", Value: source.Ref().Dict()}, {Key: "b", Value: target.Ref().Dict()}, {Key: "reason", Value: reason}}
	return p.create(ctx, KindLink, "Link "+orUntitledTitle(source.Title)+" to "+orUntitledTitle(target.Title), payload, preview,
		fingerprint(KindLink, source.ID, target.ID))
}

type update struct{ oldStr, newStr string }

func (u update) dict() py.Dict {
	return py.Dict{{Key: "old_str", Value: u.oldStr}, {Key: "new_str", Value: u.newStr}}
}

func updateDicts(updates []update) []any {
	out := make([]any, len(updates))
	for i, u := range updates {
		out[i] = u.dict()
	}
	return out
}

func applyUpdates(markdown string, updates []update) (string, bool) {
	for _, u := range updates {
		if strings.Count(markdown, u.oldStr) != 1 {
			return "", false
		}
		markdown = strings.Replace(markdown, u.oldStr, u.newStr, 1)
	}
	return markdown, true
}

func validUpdates(markdown string, raw []any) []update {
	kept := []update{}
	working := markdown
	for _, item := range raw {
		d := asDict(item)
		u := update{text(d, "old_str"), text(d, "new_str")}
		if u.oldStr != "" && u.oldStr != u.newStr && strings.Count(working, u.oldStr) == 1 {
			working = strings.Replace(working, u.oldStr, u.newStr, 1)
			kept = append(kept, u)
		}
	}
	return kept
}

func diffLines(before, after string) []any {
	old, updated := py.SplitLines(before), py.SplitLines(after)
	out := []any{}
	line := func(op, text string) any { return py.Dict{{Key: "op", Value: op}, {Key: "text", Value: text}} }
	for _, group := range py.GroupedOpcodes(old, updated, diffContext) {
		if len(out) > 0 {
			out = append(out, line("same", "…"))
		}
		for _, c := range group {
			if c.Tag == "equal" {
				for _, t := range old[c.I1:c.I2] {
					out = append(out, line("same", t))
				}
				continue
			}
			for _, t := range old[c.I1:c.I2] {
				out = append(out, line("del", t))
			}
			for _, t := range updated[c.J1:c.J2] {
				out = append(out, line("add", t))
			}
		}
	}
	return out
}

func (p *Proposer) ProposeEdit(ctx context.Context, pageID, instruction string) (*int64, error) {
	page, err := p.load(ctx, pageID)
	if err != nil {
		return nil, err
	}
	instruction = py.Strip(instruction)
	if instruction == "" {
		return nil, ProposalError("empty instruction")
	}
	markdown := page.Markdown
	user := "Instruction: " + instruction + "\n\nPage title: " + page.Title + "\nPage Markdown:\n" + py.Clip(markdown, editPageChars)
	answer, err := p.chat(ctx, editSystem, user, editSchemaJSON, editMaxTokens)
	if err != nil {
		return nil, err
	}
	raw, _ := field(answer, "updates").([]any)
	updates := validUpdates(markdown, raw)
	if len(updates) == 0 {
		return nil, ProposalError("the model proposed no applicable edit")
	}
	after, _ := applyUpdates(markdown, updates)
	summary := ""
	if s, ok := field(answer, "summary").(string); ok {
		summary = s
	}
	payload := py.Dict{
		{Key: "page_id", Value: page.ID}, {Key: "title", Value: page.Title}, {Key: "instruction", Value: instruction},
		{Key: "updates", Value: updateDicts(updates)}, {Key: "summary", Value: summary}, {Key: "snapshot", Value: page.LastEditedTime},
	}
	preview := py.Dict{
		{Key: "page", Value: page.Ref().Dict()}, {Key: "summary", Value: summary}, {Key: "instruction", Value: instruction},
		{Key: "diff", Value: diffLines(markdown, after)}, {Key: "changes", Value: updateDicts(updates)},
	}
	return p.create(ctx, KindEdit, "Edit "+orUntitledTitle(page.Title), payload, preview,
		fingerprint(KindEdit, page.ID, instruction, page.LastEditedTime, updateDicts(updates)))
}

type Applier struct {
	DB        *db.DB
	Env       config.Env
	NewClient func(token string) *Client
	Now       func() float64
}

func (a *Applier) now() float64 {
	if a.Now != nil {
		return a.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (a *Applier) client() (*Client, error) {
	token, err := config.NotionToken(a.Env)
	if err != nil {
		return nil, err
	}
	if a.NewClient != nil {
		return a.NewClient(token), nil
	}
	return NewClient(token), nil
}

func freshPage(ctx context.Context, client *Client, pageID, snapshot string) error {
	live, err := client.GetPage(ctx, pageID)
	if err != nil {
		return err
	}
	if py.Truthy(field(live, "in_trash")) {
		return StaleError("page is in the trash")
	}
	if edited := field(live, "last_edited_time"); edited != any(snapshot) {
		return StaleError("page changed since the proposal: " + snapshot + " -> " + pyStr(edited))
	}
	return nil
}

func relatedAnchor(markdown string) (string, bool, error) {
	lines := strings.Split(markdown, "\n")
	start := -1
	for i, line := range lines {
		m, err := propHeadRe.Match(line)
		if err != nil {
			return "", false, err
		}
		if start < 0 {
			if m != nil && strings.EqualFold(py.Strip(m.Group(1)), relatedHeading) {
				start = i
			}
		} else if m != nil {
			return lastListLine(lines, start, i)
		}
	}
	if start < 0 {
		return "", false, nil
	}
	return lastListLine(lines, start, len(lines))
}

func lastListLine(lines []string, start, end int) (string, bool, error) {
	for i := end - 1; i > start; i-- {
		m, err := listItemRe.Match(lines[i])
		if err != nil {
			return "", false, err
		}
		if m != nil {
			return lines[i], true, nil
		}
		if py.Strip(lines[i]) != "" {
			return "", false, nil
		}
	}
	return "", false, nil
}

func hasRelatedSection(markdown string) (bool, error) {
	for _, line := range strings.Split(markdown, "\n") {
		m, err := propHeadRe.Match(line)
		if err != nil {
			return false, err
		}
		if m != nil && strings.EqualFold(py.Strip(m.Group(1)), relatedHeading) {
			return true, nil
		}
	}
	return false, nil
}

func linkMarkdown(title, url string) string {
	return "- [" + strings.ReplaceAll(orUntitledTitle(title), "]", ")") + "](" + url + ")"
}

func linkBlocks(title, url string, withHeading bool) []any {
	bullet := py.Dict{{Key: "type", Value: "bulleted_list_item"}, {Key: "bulleted_list_item", Value: py.Dict{{Key: "rich_text", Value: []any{
		py.Dict{{Key: "type", Value: "text"}, {Key: "text", Value: py.Dict{{Key: "content", Value: orUntitledTitle(title)}, {Key: "link", Value: py.Dict{{Key: "url", Value: url}}}}}}}}}}}
	if !withHeading {
		return []any{bullet}
	}
	heading := py.Dict{{Key: "type", Value: "heading_2"}, {Key: "heading_2", Value: py.Dict{{Key: "rich_text", Value: []any{
		py.Dict{{Key: "type", Value: "text"}, {Key: "text", Value: py.Dict{{Key: "content", Value: relatedHeading}}}}}}}}}
	return []any{heading, bullet}
}

func (a *Applier) record(ctx context.Context, p *proposals.Proposal, action, undo any, applied bool, failure error) error {
	var text *string
	if failure != nil {
		s := failure.Error()
		text = &s
	}
	_, err := proposals.RecordAction(ctx, a.DB.Write, p.ID, action, undo, applied, text, a.now())
	return err
}

func payloadOf(p *proposals.Proposal) (py.Dict, error) {
	v, err := py.JSONLoads(p.Payload)
	if err != nil {
		return nil, err
	}
	return asDict(v), nil
}

func restoreUndo(pageID, previous string, edited any) py.Dict {
	return py.Dict{{Key: "op", Value: UndoRestoreOp}, {Key: "page_id", Value: pageID}, {Key: "previous_markdown", Value: previous}, {Key: "applied_edited_time", Value: edited}}
}

func (a *Applier) failed(ctx context.Context, p *proposals.Proposal, action any, err error) (py.Dict, error) {
	if rerr := a.record(ctx, p, action, nil, false, err); rerr != nil {
		return nil, rerr
	}
	return nil, err
}

func (a *Applier) ExecuteLink(ctx context.Context, p *proposals.Proposal, _ any) (py.Dict, error) {
	client, err := a.client()
	if err != nil {
		return nil, err
	}
	payload, err := payloadOf(p)
	if err != nil {
		return nil, err
	}
	failure := py.Dict{{Key: "type", Value: KindLink}, {Key: "payload", Value: payload}}
	source := text(payload, "source_id")
	previous, after, err := a.applyLink(ctx, client, payload)
	if err != nil {
		return a.failed(ctx, p, failure, err)
	}
	undo := restoreUndo(source, previous, field(after, "last_edited_time"))
	if err := a.record(ctx, p, py.Dict{{Key: "type", Value: KindLink}, {Key: "page_id", Value: source}}, undo, true, nil); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "linked", Value: field(payload, "target_id")}}, nil
}

func (a *Applier) applyLink(ctx context.Context, client *Client, payload py.Dict) (string, py.Dict, error) {
	source := text(payload, "source_id")
	if err := freshPage(ctx, client, source, text(payload, "snapshot")); err != nil {
		return "", nil, err
	}
	current, err := client.PageMarkdown(ctx, source)
	if err != nil {
		return "", nil, err
	}
	previous := text(current, "markdown")
	line := linkMarkdown(text(payload, "target_title"), text(payload, "target_url"))
	anchor, found, err := relatedAnchor(previous)
	if err != nil {
		return "", nil, err
	}
	if found && strings.Count(previous, anchor) == 1 {
		_, err = client.UpdateMarkdown(ctx, source, py.Dict{
			{Key: "type", Value: "update_content"},
			{Key: "update_content", Value: py.Dict{{Key: "content_updates", Value: []any{
				py.Dict{{Key: "old_str", Value: anchor}, {Key: "new_str", Value: anchor + "\n" + line}}}}}},
		})
	} else {
		var related bool
		if related, err = hasRelatedSection(previous); err == nil {
			_, err = client.AppendChildren(ctx, source, linkBlocks(text(payload, "target_title"), text(payload, "target_url"), !related))
		}
	}
	if err != nil {
		return "", nil, err
	}
	after, err := client.GetPage(ctx, source)
	return previous, after, err
}

func (a *Applier) ExecuteEdit(ctx context.Context, p *proposals.Proposal, _ any) (py.Dict, error) {
	client, err := a.client()
	if err != nil {
		return nil, err
	}
	payload, err := payloadOf(p)
	if err != nil {
		return nil, err
	}
	pageID := text(payload, "page_id")
	action := py.Dict{{Key: "type", Value: KindEdit}, {Key: "page_id", Value: pageID}}
	previous, after, err := a.applyEdit(ctx, client, payload)
	if err != nil {
		return a.failed(ctx, p, action, err)
	}
	edited := field(after, "last_edited_time")
	if err := a.record(ctx, p, action, restoreUndo(pageID, previous, edited), true, nil); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "edited_time", Value: edited}}, nil
}

func (a *Applier) applyEdit(ctx context.Context, client *Client, payload py.Dict) (string, py.Dict, error) {
	pageID := text(payload, "page_id")
	if err := freshPage(ctx, client, pageID, text(payload, "snapshot")); err != nil {
		return "", nil, err
	}
	current, err := client.PageMarkdown(ctx, pageID)
	if err != nil {
		return "", nil, err
	}
	previous := text(current, "markdown")
	updates := validPayloadUpdates(list(payload, "updates"))
	if _, ok := applyUpdates(previous, updates); !ok {
		return "", nil, StaleError("page text no longer matches the drafted edit")
	}
	if _, err := client.UpdateMarkdown(ctx, pageID, py.Dict{
		{Key: "type", Value: "update_content"},
		{Key: "update_content", Value: py.Dict{{Key: "content_updates", Value: field(payload, "updates")}}},
	}); err != nil {
		return "", nil, err
	}
	after, err := client.GetPage(ctx, pageID)
	return previous, after, err
}

func validPayloadUpdates(raw []any) []update {
	out := make([]update, len(raw))
	for i, item := range raw {
		d := asDict(item)
		out[i] = update{text(d, "old_str"), text(d, "new_str")}
	}
	return out
}

func (a *Applier) UndoRestore(ctx context.Context, plan json.RawMessage) (py.Dict, error) {
	v, err := py.JSONLoads(plan)
	if err != nil {
		return nil, err
	}
	info := asDict(v)
	client, err := a.client()
	if err != nil {
		return nil, err
	}
	pageID := text(info, "page_id")
	if err := freshPage(ctx, client, pageID, pyStr(field(info, "applied_edited_time"))); err != nil {
		return nil, err
	}
	if _, err := client.UpdateMarkdown(ctx, pageID, py.Dict{
		{Key: "type", Value: "replace_content"},
		{Key: "replace_content", Value: py.Dict{{Key: "new_str", Value: field(info, "previous_markdown")}}},
	}); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "restored", Value: pageID}}, nil
}

func (a *Applier) Register(r *proposals.Registry) {
	r.Executors[KindLink] = a.ExecuteLink
	r.Executors[KindEdit] = a.ExecuteEdit
	r.Undoers[UndoRestoreOp] = a.UndoRestore
}
