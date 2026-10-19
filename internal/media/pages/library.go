package pages

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/web"
)

const libraryTTLHours = media.LinkTTLSeconds / 3600

func (s *service) registerLibrary(r chi.Router) {
	r.Get("/media/library", s.libraryIndex)
	r.Get("/media/library/movie/{name}", s.libraryMovie)
	r.Get("/media/library/tv/{name}", s.libraryShow)
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
	root, err := s.settings().NeedRoot()
	if err != nil {
		s.libRender(w, r, "media_library.gohtml", LibraryPage{Error: err.Error(), Movies: []LibraryEntry{}, Shows: []LibraryEntry{}})
		return
	}
	s.libRender(w, r, "media_library.gohtml", LibraryPage{Movies: libEntries(media.MovieEntries(root)), Shows: libEntries(media.ShowEntries(root))})
}

func (s *service) libLinks(rel string) (public, vpn string) {
	if s.settings().PublicURL != "" {
		url, err := s.links.PublicURL(rel, s.now())
		if err != nil {
			panic(err)
		}
		public = url
	}
	return public, s.links.VPNURL(rel)
}

func (s *service) libraryMovie(w http.ResponseWriter, r *http.Request) {
	root, err := s.settings().NeedRoot()
	var found media.MovieView
	if err == nil {
		found, err = media.Movie(root, web.PathParam(r, "name"))
	}
	if err != nil {
		s.libMissing(w, r, err)
		return
	}
	files := make([]FileLink, len(found.Files))
	for i, f := range found.Files {
		public, vpn := s.libLinks(f.Rel)
		files[i] = FileLink{Rel: f.Rel, Name: f.Name, Size: f.Size, Public: public, VPN: vpn}
	}
	view := MovieView{Dir: found.Dir, Title: found.Title, Year: found.Year, Files: files}
	s.libRender(w, r, "media_movie.gohtml", MoviePage{Movie: view, TTLHours: libraryTTLHours})
}

func libNumber(n *int) *int64 {
	if n == nil {
		return nil
	}
	v := int64(*n)
	return &v
}

func (s *service) libraryShow(w http.ResponseWriter, r *http.Request) {
	root, err := s.settings().NeedRoot()
	var found media.ShowView
	if err == nil {
		found, err = media.Show(root, web.PathParam(r, "name"))
	}
	if err != nil {
		s.libMissing(w, r, err)
		return
	}
	seasons := make([]SeasonView, len(found.Seasons))
	for i, season := range found.Seasons {
		episodes := make([]EpisodeView, len(season.Episodes))
		for j, e := range season.Episodes {
			public, vpn := s.libLinks(e.Rel)
			episodes[j] = EpisodeView{
				Rel: e.Rel, Name: e.Name, Size: e.Size, Public: public, VPN: vpn,
				Season: libNumber(e.Season), Episode: libNumber(e.Episode),
			}
		}
		seasons[i] = SeasonView{Number: libNumber(season.Number), Dir: season.Dir, Episodes: episodes}
	}
	view := ShowView{Dir: found.Dir, Title: found.Title, Year: found.Year, Seasons: seasons}
	s.libRender(w, r, "media_show.gohtml", ShowPage{Show: view, TTLHours: libraryTTLHours})
}

func (s *service) libMissing(w http.ResponseWriter, r *http.Request, err error) {
	var gone media.NotFound
	var unset config.NotConfigured
	if !errors.As(err, &gone) && !errors.As(err, &unset) {
		panic(err)
	}
	s.deps.Error(w, r, http.StatusNotFound, "not found")
}
