package pages

import (
	"net/http"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/media"
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
	tags := ""
	if mode == dlModeMedia {
		tags = media.TagMedia
	}
	set := s.settings(r.Context())
	data, present, err := dlDecodeTorrent(torrent, set.TorrentUpload)
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
	shown, err := s.dlAdd(r, set, magnet, name, data, present, tags)
	if err != nil {
		text, kind := mustClassify(err)
		if kind != backendError {
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

func (s *service) dlAdd(r *http.Request, set media.Settings, magnet, name string, data []byte, present bool, tags string) (string, error) {
	ctx := r.Context()
	req := media.AddRequest{Magnet: magnet, Data: data, Tags: tags}
	shown := "the magnet link"
	if present {
		file := strings.TrimSpace(name)
		if file == "" {
			file = "upload.torrent"
		}
		req.Name = file
		shown = strings.TrimSpace(name)
		if shown == "" {
			shown = "the torrent"
		}
	}
	if _, err := set.Add(ctx, s.effects, set, req); err != nil {
		return "", err
	}
	if tags != "" {
		if _, err := set.SelectFiles(ctx, s.effects, set, nil); err != nil {
			return "", err
		}
	}
	return shown, nil
}
