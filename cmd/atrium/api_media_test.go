//go:build !modernc

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/media"
)

func TestMediaDownloadsAPI(t *testing.T) {
	dir := t.TempDir()
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const plain = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	eztv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"torrents": []map[string]any{{"hash": hash, "title": "Lanterns S01E01 1080p WEB", "magnet_url": mediaMagnet(hash, "Lanterns"), "size_bytes": "1000000", "season": "1", "episode": "1"}}})
	}))
	defer eztv.Close()
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, eztv.URL+"/api/get-torrents", "", ""))
	seedAllFlagsIn(t, dir)
	issued := runApp(t, "auth", "token", "issue", "--name", "media-api")
	if issued.code != cli.ExitOK {
		t.Fatalf("issue: %s", issued.stderr)
	}
	token := strings.TrimSpace(issued.stdout)
	s := &apiSite{t: t, base: startServeIn(t, dir)}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	qb.put(mnpTorrent{hash: plain, name: "Plain", content: "/hidden/agent/path", files: []mnpFile{{0, "plain.mkv", 1}}})
	get := func(path string) apiReply { return s.do(http.MethodGet, "/api/v1"+path, token, nil) }
	post := func(path string, body any) apiReply { return s.do(http.MethodPost, "/api/v1"+path, token, body) }
	before := get("/media/torrents")
	expectAPI(t, before, 200, "")
	if strings.Contains(before.body, "/hidden/") || strings.Contains(before.body, "content_path") {
		t.Fatal("download response leaks path")
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/media/torrents", token, nil, "If-None-Match", before.header.Get("ETag")), 304, "")
	invalid := post("/media/torrents", map[string]any{"magnet": "magnet:?xt=urn:btmh:1220aaaa"})
	expectAPI(t, invalid, 422, "invalid")
	if !strings.Contains(invalid.body, "magnet") || !strings.Contains(invalid.body, "fields") || qb.count("add") != 0 {
		t.Fatalf("invalid add: %s", invalid.body)
	}
	expectAPI(t, post("/media/torrents", map[string]any{"magnet": mediaMagnet(hash, "Lanterns")}), 200, "")
	if qb.count("add") != 1 {
		t.Fatalf("adds: %d", qb.count("add"))
	}
	mediaEffectID(t, d, media.OpAddMagnet, hash, "done")
	changed := s.do(http.MethodGet, "/api/v1/media/torrents", token, nil, "If-None-Match", before.header.Get("ETag"))
	expectAPI(t, changed, 200, "")
	if changed.header.Get("ETag") == before.header.Get("ETag") || !strings.Contains(changed.body, hash) {
		t.Fatalf("unchanged downloads: %s", changed.body)
	}
	writes := qb.count("torrents/filePrio")
	expectAPI(t, s.do(http.MethodPut, "/api/v1/media/torrents/"+plain+"/files", token, map[string]any{"index": 0, "wanted": false}), 404, "not_found")
	if qb.count("torrents/filePrio") != writes {
		t.Fatal("untagged priority write")
	}
	qb.put(mnpTorrent{hash: hash, name: "Lanterns", tags: []string{media.TagMedia}, files: []mnpFile{{0, "Lanterns.mkv", 1}}})
	files := get("/media/torrents/" + hash + "/files")
	expectAPI(t, files, 200, "")
	expectAPI(t, s.do(http.MethodPut, "/api/v1/media/torrents/"+hash+"/files", token, map[string]any{"index": 0, "wanted": false}), 200, "")
	selected := s.do(http.MethodGet, "/api/v1/media/torrents/"+hash+"/files", token, nil, "If-None-Match", files.header.Get("ETag"))
	expectAPI(t, selected, 200, "")
	if selected.json["items"].([]any)[0].(map[string]any)["priority"] != float64(0) || selected.header.Get("ETag") == files.header.Get("ETag") {
		t.Fatalf("selection not visible: %s", selected.body)
	}
	channel := post("/media/channels", map[string]any{"name": "Lanterns", "imdb": "tt1234567", "pattern": "1080p"})
	expectAPI(t, channel, 200, "")
	expectAPI(t, post("/media/channels/poll", nil), 200, "")
	if qb.count("add") != 1 {
		t.Fatal("channel without backfill downloaded history")
	}
	channels := get("/media/channels")
	expectAPI(t, channels, 200, "")
	if !strings.Contains(channels.body, "Lanterns") {
		t.Fatal(channels.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/media/channels", token, nil, "If-None-Match", channels.header.Get("ETag")), 304, "")
	id := channel.json["channel"].(map[string]any)["id"].(float64)
	expectAPI(t, s.do(http.MethodDelete, fmt.Sprintf("/api/v1/media/channels/%.0f", id), token, nil), 200, "")
	removed := s.do(http.MethodGet, "/api/v1/media/channels", token, nil, "If-None-Match", channels.header.Get("ETag"))
	expectAPI(t, removed, 200, "")
	if len(removed.json["items"].([]any)) != 0 || removed.header.Get("ETag") == channels.header.Get("ETag") {
		t.Fatalf("deleted channel still visible: %s", removed.body)
	}
	for i := range 105 {
		qb.put(mnpTorrent{hash: fmt.Sprintf("%040x", i+1), name: fmt.Sprint("Paged", i), tags: []string{media.TagMedia}})
	}
	page := get("/media/torrents?limit=100")
	expectAPI(t, page, 200, "")
	items := page.json["items"].([]any)
	next, ok := page.json["next"].(string)
	if len(items) != 100 || !ok {
		t.Fatalf("first page: %s", page.body)
	}
	second := get("/media/torrents?limit=100&cursor=" + url.QueryEscape(next))
	expectAPI(t, second, 200, "")
	if len(second.json["items"].([]any)) != 7 {
		t.Fatalf("second page: %s", second.body)
	}
	expectAPI(t, get("/media/torrents?q=other&cursor="+url.QueryEscape(next)), 400, "bad_cursor")
	expectAPI(t, get("/media/channels?limit=101"), 422, "invalid")
}
