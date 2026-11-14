package pages

import (
	"net/http"

	notionapp "github.com/jeiang/atrium/internal/notion/application"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) view(w http.ResponseWriter, r *http.Request) {
	partial := web.IsPartial(r)
	read, err := s.wiki.Detail(r.Context(), notionapp.DetailInput{PageID: web.PathParam(r, "page_id"), Related: !partial})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	found := read.Data.Page
	article := newPageView(found, tpl.MarkdownWith(found.Markdown, read.Data.Titles))
	if partial {
		s.fragment(w, r, http.StatusOK, "_notion_page.gohtml", articleData{NotionPage: article})
		return
	}
	s.page(w, r, "notion_page.gohtml", pageData{NotionPage: article, Related: newRelated(read.Data.Related), Back: backTo(r)})
}
