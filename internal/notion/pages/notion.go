package pages

import (
	"net/http"

	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
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
