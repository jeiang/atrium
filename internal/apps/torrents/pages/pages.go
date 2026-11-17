package pages

import (
	"context"
	"embed"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	torrentsapp "github.com/jeiang/atrium/internal/apps/torrents/application"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

//go:embed templates/*.gohtml
var templates embed.FS

const (
	modeMedia     = "media"
	modePlain     = "plain"
	downloadsPage = "/apps/media/downloads"
)

var addModes = []string{modeMedia, modePlain}

func Templates() fs.FS {
	sub, err := fs.Sub(templates, "templates")
	if err != nil {
		panic(err)
	}
	return sub
}

type Options struct {
	Service func(*web.Deps) *torrentsapp.Service
	Routes  func(*web.Deps, *torrentsapp.Service)
}

type service struct {
	deps *web.Deps
	app  *torrentsapp.Service
}

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		s := &service{deps: d, app: o.Service(d)}
		if o.Routes != nil {
			o.Routes(d, s.app)
		}
		r.Get("/apps/media/torrents/{hash}/files", s.filesGet)
		r.Post("/apps/media/torrents/{hash}/files", s.filesToggle)
		r.Get("/apps/media/add", s.addGet)
		r.Post("/apps/media/add", s.addPost)
	}
}

type FileView struct {
	Index    int    `json:"index"`
	Wanted   bool   `json:"wanted"`
	Name     string `json:"name"`
	SizeText string `json:"size_text"`
	Percent  int64  `json:"percent"`
}

type filesView struct {
	Hash        string     `json:"hash"`
	TorrentName string     `json:"torrent_name"`
	Files       []FileView `json:"files"`
	Error       string     `json:"error"`
}

type addView struct {
	Magnet      string `json:"magnet"`
	TorrentName string `json:"torrent_name"`
	AddMode     string `json:"add_mode"`
	Error       string `json:"error"`
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func (s *service) fragmentOut(w http.ResponseWriter, r *http.Request, status int, name string, data any, toast *tpl.Toast) {
	check(s.deps.Render.Fragment(w, r, status, name, tpl.Fragment{Data: data, Toast: toast}))
}

func (s *service) filesError(w http.ResponseWriter, r *http.Request, hash string, err error) {
	failure := application.Classify(err)
	web.Fail(w, r, failure, func(status int) {
		if status == http.StatusNotFound {
			s.deps.Error(w, r, status, failure.Error())
			return
		}
		view := filesView{Hash: hash, Files: []FileView{}, Error: failure.Error()}
		s.fragmentOut(w, r, backendStatus(status), "_media_files.gohtml", view, nil)
	})
}

func (s *service) files(w http.ResponseWriter, r *http.Request, hash string, toast *tpl.Toast) {
	read, err := s.app.TorrentFiles(r.Context(), torrentsapp.TorrentFilesInput{Hash: hash})
	if err != nil {
		s.filesError(w, r, hash, err)
		return
	}
	views := make([]FileView, len(read.Data.Files))
	for i, f := range read.Data.Files {
		views[i] = FileView{
			Index: f.Index, Wanted: f.Priority != contract.PrioritySkip, Name: f.Name,
			SizeText: humanSize(f.Size), Percent: int64(math.Round(f.Progress * 100)),
		}
	}
	s.fragmentOut(w, r, http.StatusOK, "_media_files.gohtml", filesView{Hash: hash, TorrentName: read.Data.Torrent.Name, Files: views}, toast)
}

func (s *service) filesGet(w http.ResponseWriter, r *http.Request) {
	s.files(w, r, web.PathParam(r, "hash"), nil)
}

func formInt(form url.Values, name string, errs *[]web.FieldError) int64 {
	raw, found := web.Value(form, name)
	if !found || raw == "" {
		*errs = append(*errs, web.Missing("body", name))
		return 0
	}
	n, ok := laxInt64(raw)
	if !ok {
		*errs = append(*errs, web.BadInt("body", name, raw))
	}
	return n
}

func (s *service) filesToggle(w http.ResponseWriter, r *http.Request) {
	hash := web.PathParam(r, "hash")
	form := web.FormValues(r)
	var errs []web.FieldError
	index := formInt(form, "index", &errs)
	wanted := formInt(form, "wanted", &errs)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	if _, err := s.app.SelectFiles(r.Context(), torrentsapp.SelectFilesInput{Hash: hash, Index: index, Wanted: wanted != 0}); err != nil {
		s.filesError(w, r, hash, err)
		return
	}
	s.files(w, r, hash, &tpl.Toast{Category: "success", Title: "Selection changed"})
}

func (s *service) addPage(w http.ResponseWriter, r *http.Request, view addView, status int, toast *tpl.Toast) {
	f, err := s.deps.Render.Frame(r, "media_add", view)
	check(err)
	f.Toast = toast
	check(s.deps.Render.Page(w, r, status, "media_add.gohtml", f))
}

func (s *service) addGet(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	view := addView{Magnet: strings.TrimSpace(web.QueryValue(query, "magnet")), TorrentName: strings.TrimSpace(web.QueryValue(query, "name")), AddMode: modeMedia}
	s.addPage(w, r, view, http.StatusOK, nil)
}

func (s *service) addPost(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	magnet, _ := web.Value(form, "magnet")
	torrent, _ := web.Value(form, "torrent")
	name, _ := web.Value(form, "name")
	mode := web.FormString(form, "mode", modeMedia)
	magnet = strings.TrimSpace(magnet)
	if !slices.Contains(addModes, mode) {
		mode = modeMedia
	}
	in := torrentsapp.AddInput{Media: mode == modeMedia}
	if magnet != "" {
		in.Magnet = &torrentsapp.Magnet{URI: magnet}
	}
	if strings.TrimSpace(torrent) != "" {
		in.Torrent = &torrentsapp.Torrent{Name: name, Encoded: torrent}
	}
	added, err := s.app.Add(r.Context(), in)
	if err != nil {
		failure := application.Classify(err)
		web.Fail(w, r, failure, func(status int) {
			s.addPage(w, r, addView{Magnet: magnet, TorrentName: name, AddMode: mode, Error: failure.Error()}, backendStatus(status), nil)
		})
		return
	}
	detail := "Plain torrent: it stays in downloads/."
	if mode == modeMedia {
		detail = "Video and subtitle files are selected once metadata arrives; it will be filed from Downloads when complete."
	}
	toast := &tpl.Toast{Category: "success", Title: "Added " + added.Data.Shown, Description: detail}
	if land(w, r, toast) {
		return
	}
	s.addPage(w, r, addView{AddMode: modeMedia}, http.StatusOK, toast)
}

func land(w http.ResponseWriter, r *http.Request, toast *tpl.Toast) bool {
	if !module.IsEnabled(r.Context(), unit.Media) {
		return false
	}
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return false
	}
	root, ok := rctx.Routes.(http.Handler)
	if !ok {
		return false
	}
	ctx := context.WithValue(web.WithToast(r.Context(), toast), chi.RouteCtxKey, (*chi.Context)(nil))
	next := r.Clone(ctx)
	next.Method = http.MethodGet
	next.URL = &url.URL{Path: downloadsPage}
	next.RequestURI = downloadsPage
	next.Body, next.ContentLength, next.Form, next.PostForm, next.MultipartForm = http.NoBody, 0, nil, nil, nil
	next.Header = r.Header.Clone()
	next.Header.Del("Content-Type")
	root.ServeHTTP(w, next)
	return true
}

func backendStatus(status int) int {
	switch status {
	case http.StatusUnprocessableEntity, http.StatusNotFound, http.StatusInternalServerError:
		return status
	}
	return http.StatusBadGateway
}

func humanSize(n *int64) string {
	size := 0.0
	if n != nil {
		size = float64(*n)
	}
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if size < 1024 || unit == "GB" {
			if unit == "B" {
				return strconv.FormatFloat(size, 'f', 0, 64) + " " + unit
			}
			return strconv.FormatFloat(size, 'f', 1, 64) + " " + unit
		}
		size /= 1024
	}
	return strconv.FormatFloat(size, 'f', 1, 64) + " GB"
}

func laxInt64(s string) (int64, bool) {
	body := strings.TrimSpace(s)
	neg := false
	if rest, ok := strings.CutPrefix(body, "-"); ok {
		body, neg = rest, true
	} else if rest, ok := strings.CutPrefix(body, "+"); ok {
		body = rest
	}
	whole, fraction, hasFraction := strings.Cut(body, ".")
	if hasFraction && (fraction == "" || strings.Trim(fraction, "0") != "") {
		return 0, false
	}
	if whole == "" || strings.HasPrefix(whole, "_") || strings.HasSuffix(whole, "_") || strings.Contains(whole, "__") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(whole, "_", ""), 10, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}
