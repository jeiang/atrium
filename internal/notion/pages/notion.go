package pages

import (
	"context"
	"errors"
	"net/http"

	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const noProposal = "No proposal was made. The pages may already be linked, or the model saw no reason to change them."

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
	near, err := notion.RelatedPages(ctx, s.deps.DB.Read, found.ID, notion.RelatedLimit)
	check(err)
	s.page(w, r, "notion_page.gohtml", pageData{NotionPage: article, Related: newRelated(near), Back: backTo(r)})
}

func (s *service) link(w http.ResponseWriter, r *http.Request) {
	target, ok := web.FormField(web.FormValues(r), "target")
	if !ok {
		web.Unprocessable(w, web.Missing("body", "target"))
		return
	}
	pageID := web.PathParam(r, "page_id")
	s.propose(w, r, func(ctx context.Context) (*int64, error) { return s.proposer.ProposeLink(ctx, pageID, target) })
}

func (s *service) edit(w http.ResponseWriter, r *http.Request) {
	instruction := web.FormString(web.FormValues(r), "instruction", "")
	pageID := web.PathParam(r, "page_id")
	s.propose(w, r, func(ctx context.Context) (*int64, error) { return s.proposer.ProposeEdit(ctx, pageID, instruction) })
}

func (s *service) propose(w http.ResponseWriter, r *http.Request, draft func(context.Context) (*int64, error)) {
	ctx := detached(r)
	id, err := draft(ctx)
	var refused notion.ProposalError
	var unavailable *httpx.ServiceUnavailable
	switch {
	case errors.As(err, &refused):
		s.fragment(w, r, http.StatusOK, "_notion_action.gohtml", actionView{Error: refused.Error()})
	case errors.As(err, &unavailable):
		s.fragment(w, r, http.StatusOK, "_notion_action.gohtml", actionView{Error: unavailable.Error()})
	case err != nil:
		panic(err)
	case id == nil:
		s.fragment(w, r, http.StatusOK, "_notion_action.gohtml", actionView{Error: noProposal})
	default:
		found, err := proposals.Get(ctx, s.deps.DB.Read, *id)
		check(err)
		s.fragment(w, r, http.StatusOK, "_notion_action.gohtml", actionView{Created: &created{ID: found.ID, Kind: found.Kind, Title: found.Title}})
	}
}
