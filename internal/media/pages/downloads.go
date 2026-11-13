package pages

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) registerDownloads(r chi.Router) {
	r.Get("/apps/media", s.dlHome)
	r.Get(dlDownloadsPage, s.dlDownloadsGet)
	r.Get("/apps/media/torrents", s.dlTorrentsGet)
	r.Get("/apps/media/torrents/{hash}/files", s.dlFilesGet)
	r.Post("/apps/media/torrents/{hash}/files", s.dlFilesToggle)
	r.Get("/apps/media/add", s.dlAddGet)
	r.Post("/apps/media/add", s.dlAddPost)
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

func (s *service) dlFragmentOut(w http.ResponseWriter, r *http.Request, status int, name string, data any, toast *tpl.Toast) {
	f, err := s.deps.Render.Frame(r, "", data)
	dlCheck(err)
	f.Toast = toast
	dlCheck(s.deps.Render.Fragment(w, r, status, name, f))
}

func (s *service) dlRead(w http.ResponseWriter, r *http.Request) (mediaapp.Downloads, bool) {
	read, err := s.app.Downloads(r.Context(), mediaapp.DownloadsInput{})
	if err != nil {
		web.Fail(w, r, err, func(status int) { s.deps.Error(w, r, status, application.Classify(err).Error()) })
		return mediaapp.Downloads{}, false
	}
	return read.Data, true
}

func torrentRows(found mediaapp.Downloads) ([]TorrentView, string) {
	rows := []TorrentView{}
	for _, t := range found.Torrents {
		rows = append(rows, torrentView(t))
	}
	if found.TorrentsError != nil {
		return rows, found.TorrentsError.Error()
	}
	return rows, ""
}

func dlAnyActive(rows []TorrentView) bool {
	for _, t := range rows {
		if t.Active {
			return true
		}
	}
	return false
}

func (s *service) dlDownloads(w http.ResponseWriter, r *http.Request, toast *tpl.Toast, status int, edited *media.View) {
	found, ok := s.dlRead(w, r)
	if !ok {
		return
	}
	rows, errText := torrentRows(found)
	waiting := []QueueView{}
	for _, it := range found.Queue {
		if edited != nil && edited.ID() == it.Item.ID {
			waiting = append(waiting, queueView(*edited))
		} else {
			waiting = append(waiting, queueView(it.View))
		}
	}
	view := downloadsView{
		Torrents: rows, Error: errText, Active: dlAnyActive(rows), Poll: dlPollSeconds,
		Queue: waiting, QueueError: found.QueueError, Kinds: found.Kinds,
	}
	s.dlPageOut(w, r, status, "media_downloads", "media_downloads.gohtml", view, toast)
}

func (s *service) dlHome(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Location", dlDownloadsPage)
	w.WriteHeader(http.StatusSeeOther)
}

func (s *service) dlDownloadsGet(w http.ResponseWriter, r *http.Request) {
	s.dlDownloads(w, r, nil, http.StatusOK, nil)
}

func (s *service) dlTorrentsGet(w http.ResponseWriter, r *http.Request) {
	found, ok := s.dlRead(w, r)
	if !ok {
		return
	}
	rows, errText := torrentRows(found)
	view := torrentsView{Torrents: rows, Error: errText, Active: dlAnyActive(rows), Poll: dlPollSeconds}
	s.dlFragmentOut(w, r, http.StatusOK, "_media_torrents.gohtml", view, nil)
}

func (s *service) dlFilesError(w http.ResponseWriter, r *http.Request, hash string, err error) {
	failure := application.Classify(err)
	web.Fail(w, r, failure, func(status int) {
		if status == http.StatusNotFound {
			s.deps.Error(w, r, status, failure.Error())
			return
		}
		view := filesView{Hash: hash, Files: []FileView{}, Error: failure.Error()}
		s.dlFragmentOut(w, r, backendStatus(status), "_media_files.gohtml", view, nil)
	})
}

func (s *service) dlFiles(w http.ResponseWriter, r *http.Request, hash string, toast *tpl.Toast) {
	read, err := s.app.TorrentFiles(r.Context(), mediaapp.TorrentFilesInput{Hash: hash})
	if err != nil {
		s.dlFilesError(w, r, hash, err)
		return
	}
	views := make([]FileView, len(read.Data.Files))
	for i, f := range read.Data.Files {
		views[i] = fileView(f)
	}
	s.dlFragmentOut(w, r, http.StatusOK, "_media_files.gohtml", filesView{Hash: hash, TorrentName: read.Data.Torrent.Name, Files: views}, toast)
}

func (s *service) dlFilesGet(w http.ResponseWriter, r *http.Request) {
	s.dlFiles(w, r, web.PathParam(r, "hash"), nil)
}

func (s *service) dlFilesToggle(w http.ResponseWriter, r *http.Request) {
	hash := web.PathParam(r, "hash")
	form := web.FormValues(r)
	var errs []web.FieldError
	var index int64
	if raw, found := web.Value(form, "index"); !found || raw == "" {
		errs = append(errs, web.Missing("body", "index"))
	} else if n, ok := laxInt64(raw); ok {
		index = n
	} else {
		errs = append(errs, web.BadInt("body", "index", raw))
	}
	var wanted int64
	if raw, found := web.Value(form, "wanted"); !found || raw == "" {
		errs = append(errs, web.Missing("body", "wanted"))
	} else if n, ok := laxInt64(raw); ok {
		wanted = n
	} else {
		errs = append(errs, web.BadInt("body", "wanted", raw))
	}
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	if _, err := s.app.SelectFiles(r.Context(), mediaapp.SelectFilesInput{Hash: hash, Index: index, Wanted: wanted != 0}); err != nil {
		s.dlFilesError(w, r, hash, err)
		return
	}
	s.dlFiles(w, r, hash, &tpl.Toast{Category: "success", Title: "Selection changed"})
}

func (s *service) dlFilePost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	form := dlLastValues(web.FormValues(r))
	set, _, root, item, err := s.dlFind(ctx, form["id"])
	if err != nil {
		text, _ := mustClassify(err)
		s.dlDownloads(w, r, &tpl.Toast{Category: "error", Title: "Not filed", Description: text}, http.StatusUnprocessableEntity, nil)
		return
	}
	plan := set.Plan(set.Edited(item, root, form), root)
	view := plan.View
	if plan.Error != "" {
		view.Error = plan.Error
		s.dlDownloads(w, r, nil, http.StatusUnprocessableEntity, &view)
		return
	}
	ops := plan.Ops
	if form["action"] != "file" {
		s.dlDownloads(w, r, nil, http.StatusOK, &view)
		return
	}
	if err := set.Apply(ctx, s.effects, set, item, ops, root); err != nil {
		view.Error = err.Error()
		s.dlDownloads(w, r, nil, http.StatusBadGateway, &view)
		return
	}
	dests := make([]string, len(ops))
	for i, op := range ops {
		dests[i] = op.Dst
	}
	s.dlDownloads(w, r, &tpl.Toast{Category: "success", Title: "Filed " + item.Name, Description: strings.Join(dests, "; ")}, http.StatusOK, nil)
}

func (s *service) dlFind(ctx context.Context, id string) (media.Settings, *media.Qbit, string, media.Item, error) {
	set := s.settings(ctx)
	client, err := set.Qbit()
	if err != nil {
		return set, nil, "", media.Item{}, err
	}
	root, err := set.NeedRoot()
	if err != nil {
		return set, nil, "", media.Item{}, err
	}
	item, err := set.Find(ctx, client, root, id)
	return set, client, root, item, err
}

func (s *service) dlIgnorePost(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	id, found := web.Value(form, "id")
	if !found || id == "" {
		web.Unprocessable(w, web.Missing("body", "id"))
		return
	}
	set, _, _, item, err := s.dlFind(r.Context(), id)
	if err == nil {
		err = media.Ignore(r.Context(), s.effects, set, item)
	}
	if err != nil {
		text, _ := mustClassify(err)
		s.dlDownloads(w, r, &tpl.Toast{Category: "error", Title: "Not ignored", Description: text}, http.StatusUnprocessableEntity, nil)
		return
	}
	s.dlDownloads(w, r, &tpl.Toast{Category: "success", Title: "Ignored", Description: "The torrent keeps seeding and is left out of the queue."}, http.StatusOK, nil)
}
