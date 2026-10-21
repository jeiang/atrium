package pages

import (
	"net/http"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) dlAddPage(w http.ResponseWriter, r *http.Request, magnet, name, mode, errText string, status int) {
	view := addView{Magnet: magnet, TorrentName: name, AddMode: mode, Error: errText, Modes: dlAddModes}
	s.dlPageOut(w, r, status, "media_add", "media_add.gohtml", view, nil)
}

func (s *service) dlAddGet(w http.ResponseWriter, r *http.Request) {
	s.dlAddPage(w, r, py.Strip(web.QueryValue(r.URL.Query(), "magnet")), "", dlModeMedia, "", http.StatusOK)
}

func (s *service) dlAddPost(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	magnet, _ := web.FormField(form, "magnet")
	torrent, _ := web.FormField(form, "torrent")
	name, _ := web.FormField(form, "name")
	mode := web.FormString(form, "mode", dlModeMedia)
	magnet = py.Strip(magnet)
	if !slices.Contains(dlAddModes, mode) {
		mode = dlModeMedia
	}
	tags := ""
	if mode == dlModeMedia {
		tags = media.TagMedia
	}
	data, present, err := dlDecodeTorrent(torrent, s.settings(r.Context()).TorrentUpload)
	if err == nil && present {
		_, err = media.TorrentHash(data)
	}
	if err != nil {
		s.dlAddPage(w, r, magnet, name, mode, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if magnet == "" && !present {
		s.dlAddPage(w, r, magnet, name, mode, "Paste a magnet link or choose a .torrent file.", http.StatusUnprocessableEntity)
		return
	}
	if magnet != "" && !strings.HasPrefix(magnet, "magnet:?") {
		s.dlAddPage(w, r, magnet, name, mode, "That is not a magnet link.", http.StatusUnprocessableEntity)
		return
	}
	shown, err := s.dlAdd(r, magnet, name, data, present, tags)
	if err != nil {
		text, soft := dlSoft(err)
		if !soft {
			panic(err)
		}
		s.dlAddPage(w, r, magnet, name, mode, text, http.StatusBadGateway)
		return
	}
	detail := "Plain torrent: it stays in downloads/."
	if mode == dlModeMedia {
		detail = "Video and subtitle files are selected once metadata arrives; it will be filed from here when complete."
	}
	s.dlDownloads(w, r, &tpl.Toast{Category: "success", Title: "Added " + shown, Description: detail}, http.StatusOK, nil)
}

func (s *service) dlAdd(r *http.Request, magnet, name string, data []byte, present bool, tags string) (string, error) {
	ctx := r.Context()
	client, err := s.dlQbit()
	if err != nil {
		return "", err
	}
	shown := "the magnet link"
	if present {
		file := py.Strip(name)
		if file == "" {
			file = "upload.torrent"
		}
		shown = py.Strip(name)
		if shown == "" {
			shown = "the torrent"
		}
		err = client.AddFile(ctx, data, file, tags)
	} else {
		err = client.AddMagnet(ctx, magnet, tags)
	}
	if err == nil && tags != "" {
		_, err = media.SelectFiles(ctx, client, nil)
	}
	if err != nil {
		return "", err
	}
	return shown, nil
}
