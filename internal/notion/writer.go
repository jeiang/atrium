package notion

import (
	"context"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

type Writer struct {
	DB     *db.DB
	Client *Client
}

func (w *Writer) Live(ctx context.Context, pageID string) (RemotePage, error) {
	page, err := w.Client.GetPage(ctx, pageID)
	if err != nil {
		return RemotePage{}, err
	}
	if page.InTrash {
		return RemotePage{}, WikiError("page is in the trash")
	}
	sensitivity, err := LoadSensitivity(ctx, w.DB.Read)
	if err != nil {
		return RemotePage{}, err
	}
	if sensitivity.IsPrivate(sensitivity.Of(page.Properties)) {
		return RemotePage{}, WikiError("page is private")
	}
	return page, nil
}

func (w *Writer) Create(ctx context.Context, parent, title, markdown string) (RemotePage, error) {
	return w.Client.CreatePage(ctx, map[string]any{
		"parent":     map[string]any{"page_id": parent},
		"properties": map[string]any{"title": map[string]any{"title": richText(title)}},
		"markdown":   markdown,
	})
}

type Edit func(body string) (string, error)

func (w *Writer) Rewrite(ctx context.Context, pageID string, edit Edit, date string, sources []string) (
	page RemotePage, previous, updated string, merged []string, edited string, err error) {
	if page, err = w.Live(ctx, pageID); err != nil {
		return
	}
	current, err := w.Client.PageMarkdown(ctx, pageID)
	if err != nil {
		return
	}
	if current.Truncated {
		err = WikiError("page is too large to rewrite safely")
		return
	}
	previous = current.Markdown
	body, _, prior, err := SplitFooter(previous)
	if err != nil {
		return
	}
	next, err := edit(body)
	if err != nil {
		return
	}
	wiki, err := LoadWiki(ctx, w.DB.Read)
	if err != nil {
		return
	}
	if updated, merged, err = wiki.WithFooter(next, date, append(append([]string{}, prior...), sources...)); err != nil {
		return
	}
	if err = w.Client.UpdateMarkdown(ctx, pageID, ReplaceContent(updated)); err != nil {
		return
	}
	after, err := w.Client.GetPage(ctx, pageID)
	if err != nil {
		return
	}
	edited = after.LastEditedTime
	return
}

func (w *Writer) Move(ctx context.Context, pageID, parent string) (page RemotePage, previous, edited string, err error) {
	if page, err = w.Live(ctx, pageID); err != nil {
		return
	}
	kind, previous := page.Parent.Ref()
	if kind != "page_id" {
		err = WikiError("only pages whose parent is a page can be moved; database rows stay where they are")
		return
	}
	if previous == parent {
		err = WikiError("the page is already under that parent")
		return
	}
	if err = w.Client.MovePage(ctx, pageID, map[string]any{"type": "page_id", "page_id": parent}); err != nil {
		return
	}
	after, err := w.Client.GetPage(ctx, pageID)
	if err != nil {
		return
	}
	edited = after.LastEditedTime
	return
}

func (w *Writer) Archive(ctx context.Context, pageID string) (page RemotePage, previous Markdown, edited string, err error) {
	if page, err = w.Live(ctx, pageID); err != nil {
		return
	}
	if previous, err = w.Client.PageMarkdown(ctx, pageID); err != nil {
		return
	}
	if err = w.Client.SetTrash(ctx, pageID, true); err != nil {
		return
	}
	after, err := w.Client.GetPage(ctx, pageID)
	if err != nil {
		return
	}
	edited = after.LastEditedTime
	return
}

func (w *Writer) Capture(ctx context.Context, lines []string) error {
	wiki, err := LoadWiki(ctx, w.DB.Read)
	if err != nil {
		return err
	}
	logID, err := wiki.LogPageID(ctx, w.DB, w.Client, wiki.Root)
	if err != nil {
		return err
	}
	_, err = PrependLog(ctx, w.Client, logID, lines)
	return err
}

func richText(text string) []any {
	return []any{map[string]any{"type": "text", "text": map[string]any{"content": text}}}
}

func ReplaceContent(text string) map[string]any {
	return map[string]any{"type": "replace_content", "replace_content": map[string]any{"new_str": text}}
}

func EditMarkdown(previous string, replace, appendText *string, section string) (string, error) {
	if section != "" {
		if replace != nil {
			updated, ok, err := ReplaceSection(previous, section, *replace)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", WikiError("page has no section titled " + section + "; use append_markdown to add one")
			}
			return updated, nil
		}
		return AppendToSection(previous, section, *appendText)
	}
	if replace != nil {
		return *replace, nil
	}
	return strings.TrimRight(previous, "\n") + "\n\n" + *appendText, nil
}
