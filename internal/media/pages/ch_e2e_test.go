package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/media"
)

var (
	chHashA = strings.Repeat("a", 40)
	chHashB = strings.Repeat("b", 40)
)

type chEztv struct {
	mu       sync.Mutex
	releases map[string][]map[string]any
	fail     int
	url      string
}

func chNewEztv(t *testing.T) *chEztv {
	t.Helper()
	f := &chEztv{releases: map[string][]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fail != 0 {
			w.WriteHeader(f.fail)
			return
		}
		list := f.releases[r.URL.Query().Get("imdb_id")]
		if list == nil {
			list = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"torrents": list})
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL + "/api/get-torrents"
	return f
}

func (f *chEztv) release(imdb, hash, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases[imdb] = append(f.releases[imdb], map[string]any{
		"hash": hash, "title": title, "magnet_url": fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=x", hash), "size_bytes": "1234567890", "season": "1", "episode": "1",
	})
}

type chTmdb struct {
	mu   sync.Mutex
	fail int
}

func chNewTmdb(t *testing.T) *chTmdb {
	t.Helper()
	f := &chTmdb{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fail != 0 {
			w.WriteHeader(f.fail)
			return
		}
		switch r.URL.Path {
		case "/search/tv":
			results := []map[string]any{}
			if strings.Contains("lanterns", strings.ToLower(r.URL.Query().Get("query"))) {
				results = append(results, map[string]any{"id": 42, "name": "Lanterns", "first_air_date": "2026-01-01", "poster_path": nil})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		case "/tv/42/external_ids":
			_ = json.NewEncoder(w).Encode(map[string]any{"imdb_id": "tt1234567"})
		case "/tv/7/external_ids":
			_ = json.NewEncoder(w).Encode(map[string]any{"imdb_id": nil})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	previous := media.TMDBAPI
	media.TMDBAPI = srv.URL
	t.Cleanup(func() { media.TMDBAPI = previous })
	return f
}

type chQbit struct {
	mu    sync.Mutex
	added []url.Values
	URL   string
}

func chNewQbit(t *testing.T) *chQbit {
	t.Helper()
	f := &chQbit{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch strings.TrimPrefix(r.URL.Path, "/api/v2/") {
		case "torrents/info":
			_, _ = w.Write([]byte("[]"))
		case "torrents/add":
			_ = r.ParseMultipartForm(1 << 20)
			f.added = append(f.added, r.Form)
			_, _ = w.Write([]byte(`{"success_count":1}`))
		}
	}))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func chNewNtfy(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func chVars(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	vars := map[string]string{
		"ATRIUM_MEDIA_MEDIA_ROOT": t.TempDir(), "ATRIUM_MEDIA_LINK_KEY": "secret-key", "NTFY_TOKEN": "tok", "ATRIUM_NOTIFY_NTFY_MEDIA_TOPIC": "media-releases",
	}
	for k, v := range extra {
		vars[k] = v
	}
	return vars
}

func chChannels(t *testing.T, s *site) []media.Channel {
	t.Helper()
	rows, err := media.ListChannels(context.Background(), s.db.Read)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func chSeen(t *testing.T, s *site, hash string) bool {
	t.Helper()
	var n int
	if err := s.db.Read.QueryRow("SELECT COUNT(*) FROM media_seen WHERE hash = ?", hash).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestChE2EChannelsSearchPreviewAddRemovePoll(t *testing.T) {
	ez := chNewEztv(t)
	chNewTmdb(t)
	qb := chNewQbit(t)
	ez.release("1234567", chHashA, "Lanterns S01E01 1080p WEB")
	ez.release("1234567", chHashB, "lanterns s01e01 720p")
	s := newSite(t, chVars(t, map[string]string{"ATRIUM_MEDIA_MEDIA_QBITTORRENT_URL": qb.URL, "ATRIUM_MEDIA_MEDIA_EZTV_URL": ez.url, "ATRIUM_NOTIFY_NTFY_URL": chNewNtfy(t), "TMDB_API_KEY": "k"}), Options{})

	empty := status(t, s.get("/media/channels"), 200, "channels page")
	contains(t, empty, "No channels", "Not checked yet.", `name="backfill" value="1" checked`, "Watching (0)")

	page := status(t, s.get("/media/channels?q=lant"), 200, "search")
	contains(t, page, "Lanterns", "tmdb_id=42", `href="/media/channels?tmdb_id=42&amp;name=Lanterns&amp;q=lant#new-channel"`)
	picked := status(t, s.get("/media/channels?tmdb_id=42&name=Lanterns"), 200, "use")
	contains(t, picked, `value="tt1234567"`, `value="42"`, "load, input changed")

	post := func(path string, form url.Values) string { return s.post(path, form).Body.String() }
	preview := post("/media/channels/preview", url.Values{"imdb": {"tt1234567"}, "pattern": {"^lanterns s01e01"}})
	contains(t, preview, "2 of 2 current EZTV releases match", `data-match="yes"`, "1.2 GB")
	strict := post("/media/channels/preview", url.Values{"imdb": {"tt1234567"}, "pattern": {"^lanterns s01e01"}, "case_sensitive": {"1"}})
	contains(t, strict, "1 of 2 current EZTV releases match", `data-match="no"`)
	blank := post("/media/channels/preview", url.Values{"imdb": {"tt1234567"}, "pattern": {"  "}})
	contains(t, blank, "0 of 2 current EZTV releases match")
	contains(t, post("/media/channels/preview", url.Values{"imdb": {""}, "pattern": {"x"}}), "Pick a show or enter an IMDb id first.")
	contains(t, post("/media/channels/preview", url.Values{"imdb": {"tt1234567"}, "pattern": {"("}}), "pattern")
	ez.mu.Lock()
	ez.fail = 500
	ez.mu.Unlock()
	contains(t, post("/media/channels/preview", url.Values{"imdb": {"tt1234567"}, "pattern": {"x"}}), "EZTV: 500")
	ez.mu.Lock()
	ez.fail = 0
	ez.mu.Unlock()

	saved := status(t, s.post("/media/channels", url.Values{"name": {"Lanterns"}, "imdb": {"tt1234567"}, "tmdb_id": {"42"}, "pattern": {"1080p"}}), 200, "add")
	contains(t, saved, "Watching Lanterns", "The first poll only records what is already out", "first check pending", "Watching (1)", `name="backfill" value="1" checked`)
	bad := status(t, s.post("/media/channels", url.Values{"name": {"Lanterns"}, "imdb": {"nope"}, "pattern": {"1080p"}, "case_sensitive": {"1"}}), 422, "bad imdb")
	contains(t, bad, "Not saved", `value="nope"`, `name="case_sensitive" value="1" checked`)
	rows := chChannels(t, s)
	if len(rows) != 1 || rows[0].TMDBID == nil || *rows[0].TMDBID != 42 {
		t.Fatalf("channels = %+v", rows)
	}

	polled := status(t, s.post("/media/channels/poll", nil), 200, "poll")
	contains(t, polled, "nothing new", "Last check")
	if !chSeen(t, s, chHashA) || chSeen(t, s, chHashB) {
		t.Fatal("first poll must record only the matching release")
	}

	backfilled := status(t, s.post("/media/channels", url.Values{"name": {"Lanterns B"}, "imdb": {"tt1234567"}, "pattern": {"720p"}, "backfill": {"1"}}), 200, "backfill")
	contains(t, backfilled, "Watching Lanterns B", "Added: lanterns s01e01 720p.")
	if len(qb.added) != 1 || qb.added[0].Get("tags") != "media" || !strings.Contains(qb.added[0].Get("urls"), chHashB) {
		t.Fatalf("qBittorrent adds = %v", qb.added)
	}

	removed := status(t, s.post(fmt.Sprintf("/media/channels/%d/delete", rows[0].ID), nil), 200, "remove")
	contains(t, removed, "Removed Lanterns")
	if len(chChannels(t, s)) != 1 {
		t.Fatal("remove must delete exactly one channel")
	}
	status(t, s.post("/media/channels/999/delete", nil), 404, "remove missing")
	status(t, s.post("/media/channels/abc/delete", nil), 422, "remove non-integer")
}

func TestChE2ETmdbFailuresAndMissingBackends(t *testing.T) {
	tm := chNewTmdb(t)
	ez := chNewEztv(t)
	s := newSite(t, chVars(t, map[string]string{"ATRIUM_MEDIA_MEDIA_EZTV_URL": ez.url, "TMDB_API_KEY": "k"}), Options{})

	missing := status(t, s.get("/media/channels?tmdb_id=7"), 200, "no imdb id")
	contains(t, missing, "No IMDb id on TMDB", "Enter it by hand.")
	none := status(t, s.get("/media/channels?q=zzz"), 200, "no results")
	contains(t, none, "No shows found.")
	tm.mu.Lock()
	tm.fail = 500
	tm.mu.Unlock()
	failed := status(t, s.get("/media/channels?q=lant"), 200, "tmdb failure")
	contains(t, failed, "TMDB failed", "TMDB: 500")

	added := status(t, s.post("/media/channels", url.Values{"name": {"Lanterns"}, "imdb": {"tt1234567"}, "pattern": {"1080p"}, "backfill": {"1"}}), 200, "backfill without qBittorrent")
	contains(t, added, "Watching Lanterns", "Nothing downloaded: media_qbittorrent_url is not set")
	if len(chChannels(t, s)) != 1 {
		t.Fatal("the channel must be kept when nothing could be downloaded")
	}
	unpolled := status(t, s.post("/media/channels/poll", nil), 422, "poll without qBittorrent")
	contains(t, unpolled, "Not polled", "media_qbittorrent_url is not set")

	plain := newSite(t, chVars(t, nil), Options{})
	page := status(t, plain.get("/media/channels?q=x"), 200, "no TMDB key")
	contains(t, page, "TMDB_API_KEY is not set: enter the IMDb id directly.", `name="imdb"`)
	lacks(t, page, `id="q"`)
}
