package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) registerDownloads(r chi.Router) {
	r.Get("/media", s.dlHome)
	r.Get(dlDownloadsPage, s.dlDownloadsGet)
	r.Get("/media/torrents", s.dlTorrentsGet)
	r.Get("/media/torrents/{hash}/files", s.dlFilesGet)
	r.Post("/media/torrents/{hash}/files", s.dlFilesToggle)
	r.Get("/media/add", s.dlAddGet)
	r.Post("/media/add", s.dlAddPost)
	r.Post("/media/file", s.dlFilePost)
	r.Post("/media/ignore", s.dlIgnorePost)
}

func dlCheck(err error) {
	if err != nil {
		panic(err)
	}
}

func dlSoft(err error) (string, bool) {
	var notConfigured config.NotConfigured
	var qbitErr media.QbitError
	if errors.As(err, &notConfigured) || errors.As(err, &qbitErr) {
		return err.Error(), true
	}
	return "", false
}

func dlLastValues(form url.Values) map[string]string {
	out := make(map[string]string, len(form))
	for key, values := range form {
		out[key] = web.LastValue(values, "")
	}
	return out
}

func (s *service) dlQbit(ctx context.Context) (*media.Qbit, error) { return s.settings(ctx).Qbit() }

func (s *service) dlRoot(ctx context.Context) (string, error) { return s.settings(ctx).NeedRoot() }

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

func (s *service) dlTorrents(ctx context.Context) ([]TorrentView, string) {
	rows := []TorrentView{}
	client, err := s.dlQbit(ctx)
	if err == nil {
		_, err = media.SelectFiles(ctx, client, nil)
	}
	var found []media.Torrent
	if err == nil {
		found, err = client.Torrents(ctx, "", "", "")
	}
	if err != nil {
		text, soft := dlSoft(err)
		if !soft {
			panic(err)
		}
		return rows, text
	}
	for _, t := range found {
		rows = append(rows, torrentView(t))
	}
	return rows, ""
}

func (s *service) dlQueue(ctx context.Context, edited *media.View) ([]QueueView, string) {
	views := []QueueView{}
	client, err := s.dlQbit(ctx)
	if err != nil {
		return views, err.Error()
	}
	root, err := s.dlRoot(ctx)
	if err != nil {
		return views, err.Error()
	}
	found, errText, err := media.Items(ctx, client, root)
	dlCheck(err)
	for _, it := range found {
		if edited != nil && edited.ID() == it.ID {
			views = append(views, queueView(*edited))
		} else {
			views = append(views, queueView(client.Planned(client.Defaults(it, root), root)))
		}
	}
	return views, errText
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
	rows, errText := s.dlTorrents(r.Context())
	waiting, queueErr := s.dlQueue(r.Context(), edited)
	view := downloadsView{
		Torrents: rows, Error: errText, Active: dlAnyActive(rows), Poll: dlPollSeconds,
		Queue: waiting, QueueError: queueErr, Kinds: media.Kinds,
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
	rows, errText := s.dlTorrents(r.Context())
	view := torrentsView{Torrents: rows, Error: errText, Active: dlAnyActive(rows), Poll: dlPollSeconds}
	s.dlFragmentOut(w, r, http.StatusOK, "_media_torrents.gohtml", view, nil)
}

func (s *service) dlFilesError(w http.ResponseWriter, r *http.Request, hash, text string) {
	view := filesView{Hash: hash, Files: []FileView{}, Error: text}
	s.dlFragmentOut(w, r, http.StatusBadGateway, "_media_files.gohtml", view, nil)
}

func (s *service) dlFiles(w http.ResponseWriter, r *http.Request, hash string, toast *tpl.Toast) {
	ctx := r.Context()
	client, err := s.dlQbit(ctx)
	var found *media.Torrent
	if err == nil {
		found, err = client.Torrent(ctx, hash)
	}
	if err == nil && found == nil {
		s.deps.Error(w, r, http.StatusNotFound, "unknown torrent")
		return
	}
	var files []media.TorrentFile
	if err == nil {
		files, err = client.Files(ctx, hash)
	}
	if err != nil {
		text, soft := dlSoft(err)
		if !soft {
			panic(err)
		}
		s.dlFilesError(w, r, hash, text)
		return
	}
	views := make([]FileView, len(files))
	for i, f := range files {
		views[i] = fileView(f)
	}
	s.dlFragmentOut(w, r, http.StatusOK, "_media_files.gohtml", filesView{Hash: hash, TorrentName: found.Name, Files: views}, toast)
}

func (s *service) dlFilesGet(w http.ResponseWriter, r *http.Request) {
	s.dlFiles(w, r, web.PathParam(r, "hash"), nil)
}

func (s *service) dlFilesToggle(w http.ResponseWriter, r *http.Request) {
	hash := web.PathParam(r, "hash")
	ctx := r.Context()
	form := web.FormValues(r)
	var errs []web.FieldError
	var index int64
	if raw, found := web.FormField(form, "index"); !found {
		errs = append(errs, web.Missing("body", "index"))
	} else if n, ok := py.LaxInt64(raw); ok {
		index = n
	} else {
		errs = append(errs, web.BadInt("body", "index", raw))
	}
	wanted := int64(1)
	if raw, found := web.FormField(form, "wanted"); found {
		if n, ok := py.LaxInt64(raw); ok {
			wanted = n
		} else {
			errs = append(errs, web.BadInt("body", "wanted", raw))
		}
	}
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	priority := media.PrioritySkip
	if wanted != 0 {
		priority = media.PriorityNormal
	}
	client, err := s.dlQbit(ctx)
	if err == nil {
		err = client.SetPriority(ctx, hash, []int{int(index)}, priority)
	}
	if err != nil {
		text, soft := dlSoft(err)
		if !soft {
			panic(err)
		}
		s.dlFilesError(w, r, hash, text)
		return
	}
	s.dlFiles(w, r, hash, &tpl.Toast{Category: "success", Title: "Selection changed"})
}

func (s *service) dlFilePost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	form := dlLastValues(web.FormValues(r))
	client, root, item, err := s.dlFind(ctx, form["id"])
	if err != nil {
		text, soft := dlSoftPlan(err)
		if !soft {
			panic(err)
		}
		s.dlDownloads(w, r, &tpl.Toast{Category: "error", Title: "Not filed", Description: text}, http.StatusUnprocessableEntity, nil)
		return
	}
	view := client.Edited(item, root, form)
	ops, err := media.Plan(&view, root)
	if err != nil {
		var planErr media.PlanError
		if !errors.As(err, &planErr) {
			panic(err)
		}
		view.Error = err.Error()
		s.dlDownloads(w, r, nil, http.StatusUnprocessableEntity, &view)
		return
	}
	if form["action"] != "file" {
		s.dlDownloads(w, r, nil, http.StatusOK, &view)
		return
	}
	if err := client.Apply(ctx, client, item, ops, root); err != nil {
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

func dlSoftPlan(err error) (string, bool) {
	var planErr media.PlanError
	if errors.As(err, &planErr) {
		return err.Error(), true
	}
	return dlSoft(err)
}

func (s *service) dlFind(ctx context.Context, id string) (*media.Qbit, string, media.Item, error) {
	client, err := s.dlQbit(ctx)
	if err != nil {
		return nil, "", media.Item{}, err
	}
	root, err := s.dlRoot(ctx)
	if err != nil {
		return nil, "", media.Item{}, err
	}
	item, err := media.Find(ctx, client, root, id)
	return client, root, item, err
}

func (s *service) dlIgnorePost(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	id, found := web.FormField(form, "id")
	if !found {
		web.Unprocessable(w, web.Missing("body", "id"))
		return
	}
	client, _, item, err := s.dlFind(r.Context(), id)
	if err == nil {
		err = media.Ignore(r.Context(), client, item)
	}
	if err != nil {
		text, soft := dlSoftPlan(err)
		if !soft {
			panic(err)
		}
		s.dlDownloads(w, r, &tpl.Toast{Category: "error", Title: "Not ignored", Description: text}, http.StatusUnprocessableEntity, nil)
		return
	}
	s.dlDownloads(w, r, &tpl.Toast{Category: "success", Title: "Ignored", Description: "The torrent keeps seeding and is left out of the queue."}, http.StatusOK, nil)
}
