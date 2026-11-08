package notion

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/llm"
)

const (
	RelatedHeading = "Related"
	linkSystem     = "You maintain cross-links between pages of the owner's personal notes workspace. You get page A and page B (title, location, opening text). Answer link=true only when the pages are clearly about the same subject, project, person or task so that a reader of A would want to open B. Similar vocabulary alone is not enough. reason is one short sentence naming what connects them."
	editSystem     = "You draft a small, targeted edit to one page of the owner's notes, written in Markdown. You get the page Markdown and an instruction. Return the minimal list of replacements: each old_str must be copied verbatim from the page and occur exactly once in it; new_str is its replacement. Change only what the instruction asks for. summary is one short sentence describing the edit. If the instruction cannot be applied as a replacement of existing text, return an empty updates list."
	linkSchemaJSON = `{"type": "object", "properties": {"link": {"type": "boolean"}, "reason": {"type": "string"}}, "required": ["link", "reason"], "additionalProperties": false}`
	editSchemaJSON = `{"type": "object", "properties": {"updates": {"type": "array", "items": {"type": "object", "properties": {"old_str": {"type": "string"}, "new_str": {"type": "string"}}, "required": ["old_str", "new_str"], "additionalProperties": false}}, "summary": {"type": "string"}}, "required": ["updates", "summary"], "additionalProperties": false}`
)

type DraftError string

func (e DraftError) Error() string { return string(e) }

type ChatFunc func(ctx context.Context, url, system, user string, schema any, maxTokens int) (json.RawMessage, error)

type Drafter struct {
	DB   *db.DB
	Chat ChatFunc
}

func (d *Drafter) chat(ctx context.Context, system, user, schema string, maxTokens int) (map[string]any, error) {
	call := d.Chat
	if call == nil {
		call = llm.ChatJSON
	}
	chatURL, err := endpoint.ChatURL(ctx, d.DB.Read)
	if err != nil {
		return nil, err
	}
	ctx, err = embed.WithModels(ctx, d.DB.Read)
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
		return nil, errors.New("draft payload is not an object")
	}
	return answer, nil
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

func (d *Drafter) load(ctx context.Context, id string) (*Page, error) {
	page, err := GetPage(ctx, d.DB.Read, id)
	if err != nil {
		return nil, err
	}
	if page == nil || page.InTrash != 0 {
		return nil, DraftError("unknown page " + id)
	}
	return page, nil
}

func orUntitledTitle(title string) string {
	if title == "" {
		return "Untitled"
	}
	return title
}

func LinkLine(title, url string) string {
	return "- [" + strings.ReplaceAll(orUntitledTitle(title), "]", ")") + "](" + url + ")"
}

type Link struct {
	SourceID string
	Title    string
	Line     string
	URL      string
}

func (d *Drafter) Link(ctx context.Context, sourceID, targetID string) (*Link, error) {
	source, err := d.load(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	target, err := d.load(ctx, targetID)
	if err != nil {
		return nil, err
	}
	if source.ID == target.ID {
		return nil, DraftError("cannot link a page to itself")
	}
	if (source.Private != 0) != (target.Private != 0) {
		return nil, DraftError("private and non-private pages are never compared")
	}
	var links []string
	if err := json.Unmarshal([]byte(source.Links), &links); err != nil {
		return nil, err
	}
	if slices.Contains(links, target.ID) {
		return nil, nil
	}
	limits, err := LoadDrafts(ctx, d.DB.Read)
	if err != nil {
		return nil, err
	}
	block := func(label string, page *Page) string {
		return label + ":\nTitle: " + page.Title + "\nLocation: " + page.Path + "\nText:\n" + clip(page.Markdown, limits.LinkPageChars)
	}
	answer, err := d.chat(ctx, linkSystem, block("Page A", source)+"\n\n"+block("Page B", target), linkSchemaJSON, limits.LinkMaxTokens)
	if err != nil {
		return nil, err
	}
	if linked, _ := answer["link"].(bool); !linked {
		return nil, nil
	}
	return &Link{SourceID: source.ID, Title: orUntitledTitle(target.Title), Line: LinkLine(target.Title, target.URL), URL: target.URL}, nil
}

func (l *Link) Apply(body string) (string, error) {
	if strings.Contains(body, "]("+l.URL+")") {
		return "", WikiError("the pages are already linked")
	}
	return AppendToSection(body, RelatedHeading, l.Line)
}

type Update struct {
	OldStr string
	NewStr string
}

type Draft struct {
	PageID  string
	Summary string
	Updates []Update
}

func validUpdates(markdown string, raw []any) []Update {
	kept := []Update{}
	working := markdown
	for _, item := range raw {
		d, _ := item.(map[string]any)
		oldStr, _ := d["old_str"].(string)
		newStr, _ := d["new_str"].(string)
		if oldStr != "" && oldStr != newStr && strings.Count(working, oldStr) == 1 {
			working = strings.Replace(working, oldStr, newStr, 1)
			kept = append(kept, Update{oldStr, newStr})
		}
	}
	return kept
}

func (d *Drafter) Edit(ctx context.Context, pageID, instruction string) (*Draft, error) {
	page, err := d.load(ctx, pageID)
	if err != nil {
		return nil, err
	}
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return nil, DraftError("empty instruction")
	}
	limits, err := LoadDrafts(ctx, d.DB.Read)
	if err != nil {
		return nil, err
	}
	user := "Instruction: " + instruction + "\n\nPage title: " + page.Title + "\nPage Markdown:\n" + clip(page.Markdown, limits.EditPageChars)
	answer, err := d.chat(ctx, editSystem, user, editSchemaJSON, limits.EditMaxTokens)
	if err != nil {
		return nil, err
	}
	raw, _ := answer["updates"].([]any)
	updates := validUpdates(page.Markdown, raw)
	if len(updates) == 0 {
		return nil, DraftError("the model drafted no applicable edit")
	}
	summary, _ := answer["summary"].(string)
	return &Draft{PageID: page.ID, Summary: summary, Updates: updates}, nil
}

func (e *Draft) Added() string {
	parts := make([]string, len(e.Updates))
	for i, u := range e.Updates {
		parts[i] = u.NewStr
	}
	return strings.Join(parts, "\n")
}

func (e *Draft) Apply(body string) (string, error) {
	for _, u := range e.Updates {
		if strings.Count(body, u.OldStr) != 1 {
			return "", WikiError("page text no longer matches the drafted edit")
		}
		body = strings.Replace(body, u.OldStr, u.NewStr, 1)
	}
	return body, nil
}
