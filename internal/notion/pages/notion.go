package pages

import (
	"context"
	"errors"
	"net/http"

	notiontools "github.com/jeiang/atrium/internal/assistant/tools/notion"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	notLinked = "Not linked. The pages may already be linked, or the model saw no reason to link them."
	linked    = "Linked"
	edited    = "Edited"
)

func (s *service) view(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	found, err := notion.PageView(ctx, s.deps.DB.Read, web.PathParam(r, "page_id"))
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown page")
		return
	}
	titles, err := notion.PageTitles(ctx, s.deps.DB.Read, []string{found.Markdown})
	check(err)
	article := newPageView(found, tpl.MarkdownWith(found.Markdown, titles))
	if web.IsPartial(r) {
		s.fragment(w, r, http.StatusOK, "_notion_page.gohtml", articleData{NotionPage: article})
		return
	}
	limits, err := notion.LoadSearch(ctx, s.deps.DB.Read)
	check(err)
	near, err := notion.RelatedPages(ctx, s.deps.DB.Read, found.ID, limits.Related)
	check(err)
	s.page(w, r, "notion_page.gohtml", pageData{NotionPage: article, Related: newRelated(near), Back: backTo(r)})
}

func (s *service) link(w http.ResponseWriter, r *http.Request) {
	target, ok := web.Value(web.FormValues(r), "target")
	if !ok || target == "" {
		web.Unprocessable(w, web.Missing("body", "target"))
		return
	}
	pageID := web.PathParam(r, "page_id")
	s.act(w, r, linked, func(ctx context.Context, tc *tool.Context) (string, error) {
		draft, err := s.drafter.Link(ctx, pageID, target)
		if err != nil {
			return "", err
		}
		if draft == nil {
			return "", notion.DraftError(notLinked)
		}
		_, err = notiontools.RewritePage(ctx, tc, draft.SourceID, draft.Line, draft.Apply)
		return draft.Title, err
	})
}

func (s *service) edit(w http.ResponseWriter, r *http.Request) {
	instruction := web.FormString(web.FormValues(r), "instruction", "")
	pageID := web.PathParam(r, "page_id")
	s.act(w, r, edited, func(ctx context.Context, tc *tool.Context) (string, error) {
		draft, err := s.drafter.Edit(ctx, pageID, instruction)
		if err != nil {
			return "", err
		}
		_, err = notiontools.RewritePage(ctx, tc, draft.PageID, draft.Added(), draft.Apply)
		return draft.Summary, err
	})
}

func (s *service) act(w http.ResponseWriter, r *http.Request, title string, run func(context.Context, *tool.Context) (string, error)) {
	ctx := detached(r)
	tc := &tool.Context{
		DB: s.deps.DB, Cfg: s.deps.Cfg, Env: s.deps.Env, Kind: tool.Chat, AllowedURLs: map[string]struct{}{}, Now: s.now,
		Undoers: toolset.Undoers(s.deps.Env),
	}
	detail, err := run(ctx, tc)
	var drafted notion.DraftError
	var unavailable *httpx.ServiceUnavailable
	if reason, refused := refusal(err); refused {
		s.fragment(w, r, http.StatusOK, "_notion_action.gohtml", actionView{Error: reason})
		return
	}
	switch {
	case errors.As(err, &drafted):
		s.fragment(w, r, http.StatusOK, "_notion_action.gohtml", actionView{Error: drafted.Error()})
	case errors.As(err, &unavailable):
		s.fragment(w, r, http.StatusOK, "_notion_action.gohtml", actionView{Error: unavailable.Error()})
	case err != nil:
		panic(err)
	default:
		s.fragment(w, r, http.StatusOK, "_notion_action.gohtml", actionView{Done: title, Detail: detail})
	}
}
