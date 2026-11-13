package pages

import (
	"net/http"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) dlAddPage(w http.ResponseWriter, r *http.Request, magnet, name, mode, errText string, status int) {
	view := addView{Magnet: magnet, TorrentName: name, AddMode: mode, Error: errText}
	s.dlPageOut(w, r, status, "media_add", "media_add.gohtml", view, nil)
}

func (s *service) dlAddGet(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	s.dlAddPage(w, r, strings.TrimSpace(web.QueryValue(query, "magnet")), strings.TrimSpace(web.QueryValue(query, "name")), dlModeMedia, "", http.StatusOK)
}

func (s *service) dlAddPost(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	magnet, _ := web.Value(form, "magnet")
	torrent, _ := web.Value(form, "torrent")
	name, _ := web.Value(form, "name")
	mode := web.FormString(form, "mode", dlModeMedia)
	magnet = strings.TrimSpace(magnet)
	if !slices.Contains(dlAddModes, mode) {
		mode = dlModeMedia
	}
	in := mediaapp.AddInput{Media: mode == dlModeMedia}
	if magnet != "" {
		in.Magnet = &mediaapp.Magnet{URI: magnet}
	}
	if strings.TrimSpace(torrent) != "" {
		in.Torrent = &mediaapp.Torrent{Name: name, Encoded: torrent}
	}
	added, err := s.app.Add(r.Context(), in)
	if err != nil {
		failure := application.Classify(err)
		web.Fail(w, r, failure, func(status int) {
			s.dlAddPage(w, r, magnet, name, mode, failure.Error(), backendStatus(status))
		})
		return
	}
	detail := "Plain torrent: it stays in downloads/."
	if mode == dlModeMedia {
		detail = "Video and subtitle files are selected once metadata arrives; it will be filed from here when complete."
	}
	s.dlDownloads(w, r, &tpl.Toast{Category: "success", Title: "Added " + added.Data.Shown, Description: detail}, http.StatusOK, nil)
}

func backendStatus(status int) int {
	switch status {
	case http.StatusUnprocessableEntity, http.StatusNotFound, http.StatusInternalServerError:
		return status
	}
	return http.StatusBadGateway
}
