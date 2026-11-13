package pages

import (
	"net/http"

	notionapp "github.com/jeiang/atrium/internal/notion/application"
	"github.com/jeiang/atrium/internal/web"
)

type wikiRow struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Path     []crumb `json:"path"`
	Private  bool    `json:"private"`
	EditedTS float64 `json:"edited_ts"`
}

type wikiData struct {
	Pages []wikiRow `json:"pages"`
	Q     string    `json:"q"`
}

func (s *service) list(w http.ResponseWriter, r *http.Request) {
	query := web.QueryValue(r.URL.Query(), "q")
	read, err := s.wiki.List(r.Context(), notionapp.ListInput{Query: query})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rows := make([]wikiRow, len(read.Data))
	for i, p := range read.Data {
		rows[i] = wikiRow{ID: p.ID, Title: p.Title, Path: crumbs(p.Path), Private: p.Private, EditedTS: p.EditedTS}
	}
	data := wikiData{Pages: rows, Q: query}
	if web.IsPartial(r) {
		s.fragment(w, r, http.StatusOK, "_library_wiki_rows.gohtml", data)
		return
	}
	f, err := s.deps.Render.Frame(r, "library_wiki", data)
	check(err)
	check(s.deps.Render.Page(w, r, http.StatusOK, "library_wiki.gohtml", f))
}
