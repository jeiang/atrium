package pages

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/web"
)

func (s *service) registerLibrary(r chi.Router) {
	r.Get("/apps/media/library", s.libraryIndex)
	r.Get("/apps/media/library/movie/{name}", s.libraryMovie)
	r.Get("/apps/media/library/tv/{name}", s.libraryShow)
}

func (s *service) libRender(w http.ResponseWriter, r *http.Request, name string, data any) {
	f, err := s.deps.Render.Frame(r, "media_library", data)
	if err != nil {
		panic(err)
	}
	if err := s.deps.Render.Page(w, r, http.StatusOK, name, f); err != nil {
		panic(err)
	}
}

func libEntries(found []media.Entry) []LibraryEntry {
	out := make([]LibraryEntry, len(found))
	for i, e := range found {
		out[i] = LibraryEntry{Kind: e.Kind, Dir: e.Dir, Title: e.Title, Year: e.Year}
	}
	return out
}

func (s *service) libraryIndex(w http.ResponseWriter, r *http.Request) {
	read, err := s.app.Library(r.Context(), mediaapp.LibraryInput{})
	if err != nil {
		web.Fail(w, r, err, func(status int) { s.deps.Error(w, r, status, application.Classify(err).Error()) })
		return
	}
	page := LibraryPage{Error: read.Data.Error, Catalogue: read.Data.Catalogue.State, Movies: libEntries(read.Data.Movies), Shows: libEntries(read.Data.Shows)}
	page.Stale = staleNotice(read.Data.Catalogue)
	s.libRender(w, r, "media_library.gohtml", page)
}

func staleNotice(c mediaapp.CatalogueStatus) string {
	if c.State != "stale" {
		return ""
	}
	return c.Problem + " Showing the library as of " + c.ObservedAt.Local().Format("2006-01-02 15:04") + "."
}

func (s *service) libDetail(w http.ResponseWriter, r *http.Request, kind string) (mediaapp.LibraryDetail, bool) {
	read, err := s.app.LibraryDetail(r.Context(), mediaapp.LibraryDetailInput{Kind: kind, Name: web.PathParam(r, "name")})
	if err != nil {
		web.Fail(w, r, err, func(status int) { s.deps.Error(w, r, status, application.Classify(err).Error()) })
		return mediaapp.LibraryDetail{}, false
	}
	return read.Data, true
}

func (s *service) libraryMovie(w http.ResponseWriter, r *http.Request) {
	found, ok := s.libDetail(w, r, media.KindMovie)
	if !ok {
		return
	}
	files := make([]FileLink, len(found.Files))
	for i, f := range found.Files {
		files[i] = FileLink{Rel: f.Rel, Name: f.Name, Size: f.Size, Public: f.Public, VPN: f.VPN}
	}
	view := MovieView{Dir: found.Entry.Dir, Title: found.Entry.Title, Year: found.Entry.Year, Files: files}
	s.libRender(w, r, "media_movie.gohtml", MoviePage{Movie: view, TTL: linkSpan(found.LinkTTL), Stale: staleNotice(found.Catalogue)})
}

func (s *service) libraryShow(w http.ResponseWriter, r *http.Request) {
	found, ok := s.libDetail(w, r, media.KindShow)
	if !ok {
		return
	}
	seasons := make([]SeasonView, len(found.Seasons))
	for i, season := range found.Seasons {
		episodes := make([]EpisodeView, len(season.Episodes))
		for j, e := range season.Episodes {
			episodes[j] = EpisodeView{
				Rel: e.Rel, Name: e.Name, Size: e.Size, Public: e.Public, VPN: e.VPN,
				Season: e.Season, Episode: e.Episode,
			}
		}
		seasons[i] = SeasonView{Number: season.Number, Dir: season.Dir, Episodes: episodes}
	}
	view := ShowView{Dir: found.Entry.Dir, Title: found.Entry.Title, Year: found.Entry.Year, Seasons: seasons}
	s.libRender(w, r, "media_show.gohtml", ShowPage{Show: view, TTL: linkSpan(found.LinkTTL), Stale: staleNotice(found.Catalogue)})
}
