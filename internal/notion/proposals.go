package notion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"regexp"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/difflib"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

const (
	KindLink       = "notion_link"
	KindEdit       = "notion_edit"
	KindCapture    = "notion_capture"
	UndoRestoreOp  = "notion_restore"
	relatedHeading = "Related"
	diffContext    = 2
	linkSystem     = "You maintain cross-links between pages of the owner's personal notes workspace. You get page A and page B (title, location, opening text). Answer link=true only when the pages are clearly about the same subject, project, person or task so that a reader of A would want to open B. Similar vocabulary alone is not enough. reason is one short sentence naming what connects them."
	editSystem     = "You draft a small, targeted edit to one page of the owner's notes, written in Markdown. You get the page Markdown and an instruction. Return the minimal list of replacements: each old_str must be copied verbatim from the page and occur exactly once in it; new_str is its replacement. Change only what the instruction asks for. summary is one short sentence describing the edit. If the instruction cannot be applied as a replacement of existing text, return an empty updates list."
	linkSchemaJSON = `{"type": "object", "properties": {"link": {"type": "boolean"}, "reason": {"type": "string"}}, "required": ["link", "reason"], "additionalProperties": false}`
	editSchemaJSON = `{"type": "object", "properties": {"updates": {"type": "array", "items": {"type": "object", "properties": {"old_str": {"type": "string"}, "new_str": {"type": "string"}}, "required": ["old_str", "new_str"], "additionalProperties": false}}, "summary": {"type": "string"}}, "required": ["updates", "summary"], "additionalProperties": false}`
)

var (
	propHeadRe = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*#*\s*$`)
	listItemRe = regexp.MustCompile(`^\s*(?:[-*+]|\d+\.)\s`)
)

type ProposalError string

func (e ProposalError) Error() string { return string(e) }

type StaleError string

func (e StaleError) Error() string { return string(e) }

type ChatFunc func(ctx context.Context, url, system, user string, schema any, maxTokens int) (json.RawMessage, error)

func (p *Page) Ref() PageRef { return pageRef(p.ID, p.Title, p.URL, p.Path, p.Private != 0) }

func (r PageRef) Map() map[string]any {
	path := make([]any, len(r.Path))
	for i, part := range r.Path {
		path[i] = map[string]any{"title": part.Title}
	}
	return map[string]any{"id": r.ID, "title": r.Title, "url": r.URL, "path": path, "private": r.Private}
}

type Proposer struct {
	DB   *db.DB
	Chat ChatFunc
	Now  func() float64
}

func (p *Proposer) now() float64 {
	if p.Now != nil {
		return p.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (p *Proposer) chat(ctx context.Context, system, user, schema string, maxTokens int) (map[string]any, error) {
	call := p.Chat
	if call == nil {
		call = llm.ChatJSON
	}
	chatURL, err := endpoint.ChatURL(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	ctx, err = embed.WithModels(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	raw, err := call(ctx, chatURL, system, user, json.RawMessage(schema), maxTokens)
	if err != nil {
		return nil, err
	}
	var answer map[string]any
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, err
	}
	if answer == nil {
		return nil, errors.New("proposal payload is not an object")
	}
	return answer, nil
}

func fingerprint(parts ...any) string {
	quoted := make([]string, len(parts))
	for i, part := range parts {
		encoded, _ := json.Marshal(part)
		quoted[i] = string(encoded)
	}
	sum := sha256.Sum256([]byte("[" + strings.Join(quoted, ", ") + "]"))
	return hex.EncodeToString(sum[:])[:32]
}

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n")
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

func (p *Proposer) create(ctx context.Context, kind, title string, payload, preview map[string]any, fp string) (*int64, error) {
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
	var links []string
	if err := json.Unmarshal([]byte(source.Links), &links); err != nil {
		return nil, err
	}
	if slices.Contains(links, target.ID) {
		return nil, nil
	}
	limits, err := LoadProposals(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	block := func(label string, page *Page) string {
		return label + ":\nTitle: " + page.Title + "\nLocation: " + page.Path + "\nText:\n" + clip(page.Markdown, limits.LinkPageChars)
	}
	answer, err := p.chat(ctx, linkSystem, block("Page A", source)+"\n\n"+block("Page B", target), linkSchemaJSON, limits.LinkMaxTokens)
	if err != nil {
		return nil, err
	}
	if !present(answer["link"]) {
		return nil, nil
	}
	reason, _ := answer["reason"].(string)
	payload := map[string]any{
		"source_id": source.ID, "target_id": target.ID,
		"source_title": source.Title, "target_title": target.Title,
		"target_url": target.URL, "reason": reason, "snapshot": source.LastEditedTime,
	}
	preview := map[string]any{"a": source.Ref().Map(), "b": target.Ref().Map(), "reason": reason}
	return p.create(ctx, KindLink, "Link "+orUntitledTitle(source.Title)+" to "+orUntitledTitle(target.Title), payload, preview,
		fingerprint(KindLink, source.ID, target.ID))
}

type update struct {
	OldStr string `json:"old_str"`
	NewStr string `json:"new_str"`
}

func updateDicts(updates []update) []any {
	out := make([]any, len(updates))
	for i, u := range updates {
		out[i] = map[string]any{"old_str": u.OldStr, "new_str": u.NewStr}
	}
	return out
}

func applyUpdates(markdown string, updates []update) (string, bool) {
	for _, u := range updates {
		if strings.Count(markdown, u.OldStr) != 1 {
			return "", false
		}
		markdown = strings.Replace(markdown, u.OldStr, u.NewStr, 1)
	}
	return markdown, true
}

func validUpdates(markdown string, raw []any) []update {
	kept := []update{}
	working := markdown
	for _, item := range raw {
		d, _ := item.(map[string]any)
		u := update{mapString(d, "old_str"), mapString(d, "new_str")}
		if u.OldStr != "" && u.OldStr != u.NewStr && strings.Count(working, u.OldStr) == 1 {
			working = strings.Replace(working, u.OldStr, u.NewStr, 1)
			kept = append(kept, u)
		}
	}
	return kept
}

func diffLines(before, after string) []any {
	old, updated := splitLines(before), splitLines(after)
	out := []any{}
	line := func(op, text string) any { return map[string]any{"op": op, "text": text} }
	for _, group := range difflib.GroupedOpcodes(old, updated, diffContext) {
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
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return nil, ProposalError("empty instruction")
	}
	limits, err := LoadProposals(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	markdown := page.Markdown
	user := "Instruction: " + instruction + "\n\nPage title: " + page.Title + "\nPage Markdown:\n" + clip(markdown, limits.EditPageChars)
	answer, err := p.chat(ctx, editSystem, user, editSchemaJSON, limits.EditMaxTokens)
	if err != nil {
		return nil, err
	}
	raw, _ := answer["updates"].([]any)
	updates := validUpdates(markdown, raw)
	if len(updates) == 0 {
		return nil, ProposalError("the model proposed no applicable edit")
	}
	after, _ := applyUpdates(markdown, updates)
	summary, _ := answer["summary"].(string)
	payload := map[string]any{
		"page_id": page.ID, "title": page.Title, "instruction": instruction,
		"updates": updateDicts(updates), "summary": summary, "snapshot": page.LastEditedTime,
	}
	preview := map[string]any{
		"page": page.Ref().Map(), "summary": summary, "instruction": instruction,
		"diff": diffLines(markdown, after), "changes": updateDicts(updates),
	}
	return p.create(ctx, KindEdit, "Edit "+orUntitledTitle(page.Title), payload, preview,
		fingerprint(KindEdit, page.ID, instruction, page.LastEditedTime, updateDicts(updates)))
}

type Applier struct {
	DB  *db.DB
	Env config.Env
	Now func() float64
}

func (a *Applier) now() float64 {
	if a.Now != nil {
		return a.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (a *Applier) client(ctx context.Context) (*Client, error) {
	return NewClient(ctx, a.DB.Read, a.Env)
}

func freshPage(ctx context.Context, client *Client, pageID, snapshot string) error {
	live, err := client.GetPage(ctx, pageID)
	if err != nil {
		return err
	}
	if live.InTrash {
		return StaleError("page is in the trash")
	}
	if live.LastEditedTime != snapshot {
		return StaleError("page changed since the proposal: " + snapshot + " -> " + live.LastEditedTime)
	}
	return nil
}

func relatedAnchor(markdown string) (string, bool, error) {
	lines := strings.Split(markdown, "\n")
	start := -1
	for i, line := range lines {
		loc := propHeadRe.FindStringSubmatchIndex(line)
		if start < 0 {
			if loc != nil && strings.EqualFold(strings.TrimSpace(line[loc[2]:loc[3]]), relatedHeading) {
				start = i
			}
		} else if loc != nil {
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
		if loc := listItemRe.FindStringIndex(lines[i]); loc != nil && loc[0] == 0 {
			return lines[i], true, nil
		}
		if strings.TrimSpace(lines[i]) != "" {
			return "", false, nil
		}
	}
	return "", false, nil
}

func hasRelatedSection(markdown string) (bool, error) {
	for _, line := range strings.Split(markdown, "\n") {
		loc := propHeadRe.FindStringSubmatchIndex(line)
		if loc != nil && strings.EqualFold(strings.TrimSpace(line[loc[2]:loc[3]]), relatedHeading) {
			return true, nil
		}
	}
	return false, nil
}

func linkMarkdown(title, url string) string {
	return "- [" + strings.ReplaceAll(orUntitledTitle(title), "]", ")") + "](" + url + ")"
}

func linkBlocks(title, url string, withHeading bool) []any {
	bullet := map[string]any{"type": "bulleted_list_item", "bulleted_list_item": map[string]any{"rich_text": []any{
		map[string]any{"type": "text", "text": map[string]any{"content": orUntitledTitle(title), "link": map[string]any{"url": url}}}}}}
	if !withHeading {
		return []any{bullet}
	}
	heading := map[string]any{"type": "heading_2", "heading_2": map[string]any{"rich_text": []any{
		map[string]any{"type": "text", "text": map[string]any{"content": relatedHeading}}}}}
	return []any{heading, bullet}
}

func (a *Applier) record(ctx context.Context, p *proposals.Proposal, action, undo any, applied bool, failure error) error {
	var text *string
	if failure != nil {
		s := failure.Error()
		text = &s
	}
	state := ledger.OK
	if !applied {
		state = ledger.Failed
	}
	_, err := proposals.RecordAction(ctx, a.DB.Write, p.ID, "write", nil, action, undo, state, text, a.now())
	return err
}

func present(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) != ""
	case bool:
		return v
	}
	return true
}

func mapString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func payloadOf(p *proposals.Proposal) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal(p.Payload, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func restoreUndo(pageID, previous, edited string) map[string]any {
	return map[string]any{"op": UndoRestoreOp, "page_id": pageID, "previous_markdown": previous, "applied_edited_time": edited}
}

func (a *Applier) failed(ctx context.Context, p *proposals.Proposal, action any, err error) (any, error) {
	if rerr := a.record(ctx, p, action, nil, false, err); rerr != nil {
		return nil, rerr
	}
	return nil, err
}

func (a *Applier) ExecuteLink(ctx context.Context, p *proposals.Proposal, _ any) (any, error) {
	client, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	payload, err := payloadOf(p)
	if err != nil {
		return nil, err
	}
	failure := map[string]any{"type": KindLink, "payload": payload}
	source := mapString(payload, "source_id")
	previous, after, err := a.applyLink(ctx, client, payload)
	if err != nil {
		return a.failed(ctx, p, failure, err)
	}
	undo := restoreUndo(source, previous, after.LastEditedTime)
	if err := a.record(ctx, p, map[string]any{"type": KindLink, "page_id": source}, undo, true, nil); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "linked", Value: payload["target_id"]}}, nil
}

func (a *Applier) applyLink(ctx context.Context, client *Client, payload map[string]any) (string, RemotePage, error) {
	source := mapString(payload, "source_id")
	if err := freshPage(ctx, client, source, mapString(payload, "snapshot")); err != nil {
		return "", RemotePage{}, err
	}
	current, err := client.PageMarkdown(ctx, source)
	if err != nil {
		return "", RemotePage{}, err
	}
	previous := current.Markdown
	line := linkMarkdown(mapString(payload, "target_title"), mapString(payload, "target_url"))
	anchor, found, err := relatedAnchor(previous)
	if err != nil {
		return "", RemotePage{}, err
	}
	if found && strings.Count(previous, anchor) == 1 {
		err = client.UpdateMarkdown(ctx, source, map[string]any{
			"type": "update_content",
			"update_content": map[string]any{"content_updates": []any{
				map[string]any{"old_str": anchor, "new_str": anchor + "\n" + line}}},
		})
	} else {
		var related bool
		if related, err = hasRelatedSection(previous); err == nil {
			err = client.AppendChildren(ctx, source, linkBlocks(mapString(payload, "target_title"), mapString(payload, "target_url"), !related))
		}
	}
	if err != nil {
		return "", RemotePage{}, err
	}
	after, err := client.GetPage(ctx, source)
	return previous, after, err
}

func (a *Applier) ExecuteEdit(ctx context.Context, p *proposals.Proposal, _ any) (any, error) {
	client, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	payload, err := payloadOf(p)
	if err != nil {
		return nil, err
	}
	pageID := mapString(payload, "page_id")
	action := map[string]any{"type": KindEdit, "page_id": pageID}
	previous, after, err := a.applyEdit(ctx, client, payload)
	if err != nil {
		return a.failed(ctx, p, action, err)
	}
	if err := a.record(ctx, p, action, restoreUndo(pageID, previous, after.LastEditedTime), true, nil); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "edited_time", Value: after.LastEditedTime}}, nil
}

func (a *Applier) applyEdit(ctx context.Context, client *Client, payload map[string]any) (string, RemotePage, error) {
	pageID := mapString(payload, "page_id")
	if err := freshPage(ctx, client, pageID, mapString(payload, "snapshot")); err != nil {
		return "", RemotePage{}, err
	}
	current, err := client.PageMarkdown(ctx, pageID)
	if err != nil {
		return "", RemotePage{}, err
	}
	previous := current.Markdown
	updates := validPayloadUpdates(payload["updates"])
	if _, ok := applyUpdates(previous, updates); !ok {
		return "", RemotePage{}, StaleError("page text no longer matches the drafted edit")
	}
	if err := client.UpdateMarkdown(ctx, pageID, map[string]any{
		"type":           "update_content",
		"update_content": map[string]any{"content_updates": payload["updates"]},
	}); err != nil {
		return "", RemotePage{}, err
	}
	after, err := client.GetPage(ctx, pageID)
	return previous, after, err
}

func validPayloadUpdates(raw any) []update {
	items, _ := raw.([]any)
	out := make([]update, len(items))
	for i, item := range items {
		d, _ := item.(map[string]any)
		out[i] = update{mapString(d, "old_str"), mapString(d, "new_str")}
	}
	return out
}

func (a *Applier) UndoRestore(ctx context.Context, plan json.RawMessage) (any, error) {
	var info struct {
		PageID            string `json:"page_id"`
		PreviousMarkdown  string `json:"previous_markdown"`
		AppliedEditedTime string `json:"applied_edited_time"`
	}
	if err := json.Unmarshal(plan, &info); err != nil {
		return nil, err
	}
	client, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	pageID := info.PageID
	if err := freshPage(ctx, client, pageID, info.AppliedEditedTime); err != nil {
		return nil, err
	}
	if err := client.UpdateMarkdown(ctx, pageID, map[string]any{
		"type":            "replace_content",
		"replace_content": map[string]any{"new_str": info.PreviousMarkdown},
	}); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "restored", Value: pageID}}, nil
}

func (a *Applier) ExecuteCapture(ctx context.Context, p *proposals.Proposal, _ any) (any, error) {
	var payload struct {
		Note string `json:"note"`
	}
	if err := json.Unmarshal([]byte(p.Payload), &payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.Note) == "" {
		return nil, ProposalError("the wiki note is empty")
	}
	client, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	wiki, err := LoadWiki(ctx, a.DB.Read)
	if err != nil {
		return nil, err
	}
	logID, err := wiki.LogPageID(ctx, a.DB, client, wiki.Root)
	if err != nil {
		return nil, err
	}
	before, err := client.PageMarkdown(ctx, logID)
	if err != nil {
		return nil, err
	}
	if before.Truncated {
		return nil, WikiError("the Wiki Log page is too large to update safely; archive it and let atrium start a new one")
	}
	action := map[string]any{"type": KindCapture, "page_id": logID, "note": payload.Note}
	id, err := proposals.BeginAction(ctx, a.DB.Write, p.ID, action, a.now())
	if err != nil {
		return nil, err
	}
	writer := &Writer{DB: a.DB, Client: client}
	date := time.Unix(int64(a.now()), 0).UTC().Format("2006-01-02")
	if err := writer.Capture(ctx, []string{LogLine(date, "note", payload.Note, "")}); err != nil {
		text := err.Error()
		_ = proposals.SettleAction(ctx, a.DB.Write, id, action, nil, ledger.Failed, &text, a.now())
		return nil, err
	}
	after, err := client.GetPage(ctx, logID)
	if err != nil {
		text := err.Error()
		_ = proposals.SettleAction(ctx, a.DB.Write, id, action, nil, ledger.Failed, &text, a.now())
		return nil, err
	}
	if err := proposals.SettleAction(ctx, a.DB.Write, id, action, restoreUndo(logID, before.Markdown, after.LastEditedTime), ledger.OK, nil, a.now()); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "summary", Value: "Wiki note added: " + payload.Note}, {Key: "page_id", Value: logID}}, nil
}

func (a *Applier) Register(r *proposals.Registry, owner string) {
	r.Executors[KindLink] = a.ExecuteLink
	r.Executors[KindEdit] = a.ExecuteEdit
	r.Undoers[UndoRestoreOp] = a.UndoRestore
	r.Executors[KindCapture] = a.ExecuteCapture
	r.AddKind(proposals.Kind{Name: KindCapture, Module: owner, Label: "Wiki note", Icon: "notebook-pen"})
	r.AddKind(proposals.Kind{Name: KindLink, Module: owner, Label: "Notion link", Icon: "link"})
	r.AddKind(proposals.Kind{Name: KindEdit, Module: owner, Label: "Notion edit", Icon: "pencil"})
}
