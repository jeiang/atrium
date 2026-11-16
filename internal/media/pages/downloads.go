package pages

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) registerDownloads(r chi.Router) {
	r.Get("/apps/media", s.dlHome)
	r.Get(dlDownloadsPage, s.dlDownloadsGet)
	r.Post("/apps/media/file", s.dlFilePost)
	r.Post("/apps/media/ignore", s.dlIgnorePost)
}

func dlCheck(err error) {
	if err != nil {
		panic(err)
	}
}

func dlLastValues(form url.Values) map[string]string {
	out := make(map[string]string, len(form))
	for key := range form {
		out[key] = web.ValueOr(form, key, "")
	}
	return out
}

func (s *service) dlPageOut(w http.ResponseWriter, r *http.Request, status int, page, name string, data any, toast *tpl.Toast) {
	f, err := s.deps.Render.Frame(r, page, data)
	dlCheck(err)
	f.Toast = toast
	dlCheck(s.deps.Render.Page(w, r, status, name, f))
}


func (s *service) dlDownloads(w http.ResponseWriter, r *http.Request, toast *tpl.Toast, status int, edited *mediaapp.View) {
	read, err := s.app.Queue(r.Context(), mediaapp.QueueInput{})
	if err != nil {
		web.Fail(w, r, err, func(status int) { s.deps.Error(w, r, status, application.Classify(err).Error()) })
		return
	}
	found := read.Data
	waiting := []QueueView{}
	for _, it := range found.Queue {
		if edited != nil && edited.ID() == it.Item.ID {
			waiting = append(waiting, queueView(*edited))
		} else {
			waiting = append(waiting, queueView(it.View))
		}
	}
	view := downloadsView{Queue: waiting, QueueError: found.QueueError, Kinds: found.Kinds}
	s.dlPageOut(w, r, status, "media_downloads", "media_downloads.gohtml", view, toast)
}

func (s *service) dlHome(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Location", dlDownloadsPage)
	w.WriteHeader(http.StatusSeeOther)
}

func (s *service) dlDownloadsGet(w http.ResponseWriter, r *http.Request) {
	s.dlDownloads(w, r, nil, http.StatusOK, nil)
}

func (s *service) dlFilePost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	form := dlLastValues(web.FormValues(r))
	in := mediaapp.FilingInput{ID: form["id"], Form: form}
	found, err := s.app.FilingPreview(ctx, in)
	if err != nil {
		s.dlRefused(w, r, "Not filed", err)
		return
	}
	view := found.Data.View
	if found.Data.Error != "" {
		view.Error = found.Data.Error
		s.dlDownloads(w, r, nil, http.StatusUnprocessableEntity, &view)
		return
	}
	if form["action"] != "file" {
		s.dlDownloads(w, r, nil, http.StatusOK, &view)
		return
	}
	filed, err := s.app.FileItem(ctx, in)
	if err != nil {
		view.Error = err.Error()
		s.dlDownloads(w, r, nil, http.StatusBadGateway, &view)
		return
	}
	s.dlDownloads(w, r, &tpl.Toast{Category: "success", Title: "Filed " + filed.Data.Item.Name, Description: strings.Join(filed.Data.Dests, "; ")}, http.StatusOK, nil)
}

func (s *service) dlRefused(w http.ResponseWriter, r *http.Request, title string, err error) {
	failure := application.Classify(err)
	if failure.Code == application.Internal || failure.Code == application.Canceled {
		web.Fail(w, r, err, func(status int) { s.deps.Error(w, r, status, failure.Error()) })
		return
	}
	s.dlDownloads(w, r, &tpl.Toast{Category: "error", Title: title, Description: failure.Error()}, http.StatusUnprocessableEntity, nil)
}

func (s *service) dlIgnorePost(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	id, found := web.Value(form, "id")
	if !found || id == "" {
		web.Unprocessable(w, web.Missing("body", "id"))
		return
	}
	if _, err := s.app.Ignore(r.Context(), mediaapp.IgnoreInput{ID: id}); err != nil {
		s.dlRefused(w, r, "Not ignored", err)
		return
	}
	s.dlDownloads(w, r, &tpl.Toast{Category: "success", Title: "Ignored", Description: "The torrent keeps seeding and is left out of the queue."}, http.StatusOK, nil)
}
