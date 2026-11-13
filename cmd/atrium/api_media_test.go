//go:build !modernc

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
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
	writes = qb.count("torrents/filePrio")
	for _, body := range []any{nil, map[string]any{}, map[string]any{"wanted": true}} {
		missing := s.do(http.MethodPut, "/api/v1/media/torrents/"+hash+"/files", token, body)
		expectAPI(t, missing, 422, "invalid")
		if !strings.Contains(missing.body, `"index"`) || qb.count("torrents/filePrio") != writes {
			t.Fatalf("missing index: %s", missing.body)
		}
	}
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
	pair := []string{strings.Repeat("c", 40), strings.Repeat("e", 40)}
	for _, h := range pair {
		qb.put(mnpTorrent{hash: h, name: "Pair " + h[:4], tags: []string{media.TagMedia}, files: []mnpFile{{0, "a.mkv", 1}, {1, "b.mkv", 1}}})
	}
	onePage := get("/media/torrents/" + pair[0] + "/files?limit=1")
	expectAPI(t, onePage, 200, "")
	fileCursor, ok := onePage.json["next"].(string)
	if !ok {
		t.Fatalf("file page: %s", onePage.body)
	}
	expectAPI(t, get("/media/torrents/"+pair[0]+"/files?limit=1&cursor="+url.QueryEscape(fileCursor)), 200, "")
	expectAPI(t, get("/media/torrents/"+pair[1]+"/files?limit=1&cursor="+url.QueryEscape(fileCursor)), 400, "bad_cursor")
}

func mediaAPIToken(t *testing.T) string {
	t.Helper()
	issued := runApp(t, "auth", "token", "issue", "--name", "media-library-api")
	if issued.code != cli.ExitOK {
		t.Fatalf("issue: %s", issued.stderr)
	}
	return strings.TrimSpace(issued.stdout)
}

func mediaAPIID(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestMediaLibraryAPILinksAndFiling(t *testing.T) {
	const hash = "dddddddddddddddddddddddddddddddddddddddd"
	const untagged = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	const skipped = "ffffffffffffffffffffffffffffffffffffffff"
	const movie = "movies/" + libraryMovie + "/" + libraryMovie + ".mkv"
	dir := t.TempDir()
	qb, qbURL := mnpNewQbit(t)
	agent, root := agentLibrary(t, dir, qbURL)
	body := strings.Repeat("abcdefghij", 10)
	mnpWrite(t, filepath.Join(root, movie), []byte(body))
	mnpWrite(t, filepath.Join(root, libraryEpisode), []byte("episode bytes"))
	mnpWrite(t, filepath.Join(root, "downloads", "secret.mkv"), []byte("do not serve"))
	content := filepath.Join(root, "downloads", "Arrival.2016.1080p.WEB")
	mnpWrite(t, filepath.Join(content, "Arrival.2016.mkv"), []byte("arrival"))
	qb.put(mnpTorrent{hash: hash, name: "Arrival.2016.1080p.WEB", tags: []string{media.TagMedia}, state: media.StoppedUP, progress: 1, content: content})
	heat := filepath.Join(root, "downloads", "Heat.1995.1080p.WEB")
	mnpWrite(t, filepath.Join(heat, "Heat.1995.mkv"), []byte("heat"))
	qb.put(mnpTorrent{hash: skipped, name: "Heat.1995.1080p.WEB", tags: []string{media.TagMedia}, state: media.StoppedUP, progress: 1, content: heat})
	qb.put(mnpTorrent{hash: untagged, name: "Heat.1995.1080p.WEB", state: media.StoppedUP, progress: 1, content: heat})
	setEnv(t, settingEnv(config.KeyPublicURL), "https://atrium.test")
	setEnv(t, settingEnv(media.KeyVPNBaseURL), "https://artemis.vpn:8443")
	seedAllFlagsIn(t, dir)
	if got := runApp(t, "settings", "set", media.KeyLinkTTL, "60"); got.code != cli.ExitOK {
		t.Fatalf("link ttl: %s", got.stderr)
	}
	token := mediaAPIToken(t)
	s := &apiSite{t: t, base: startServeIn(t, dir)}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	get := func(path string, headers ...string) apiReply {
		return s.do(http.MethodGet, "/api/v1"+path, token, nil, headers...)
	}
	private := func(r apiReply) {
		t.Helper()
		for _, secret := range []string{root, agent.dir, agent.base, strings.TrimPrefix(agent.base, "http://"), "downloads/", "do not serve"} {
			if strings.Contains(r.body, secret) {
				t.Fatalf("response leaks %q: %s", secret, r.body)
			}
		}
	}

	for _, target := range []string{"GET /media/library", "GET /media/files/" + mediaAPIID(movie) + "/link", "GET /media/filings/" + hash, "POST /media/sweep"} {
		method, path, _ := strings.Cut(target, " ")
		expectAPI(t, s.do(method, "/api/v1"+path, "", nil), http.StatusUnauthorized, "unauthenticated")
		expectAPI(t, s.do(method, "/api/v1"+path, "", nil, "Remote-Groups", "atrium_access,atrium_admin", "Remote-User", "owner"), http.StatusUnauthorized, "unauthenticated")
	}
	lib := get("/media/library")
	expectAPI(t, lib, 200, "")
	private(lib)
	cat := lib.json["catalogue"].(map[string]any)
	if cat["stale"] != false || cat["state"] != "current" || len(lib.json["items"].([]any)) != 2 {
		t.Fatalf("library: %s", lib.body)
	}
	expectAPI(t, get("/media/library", "If-None-Match", lib.header.Get("ETag")), 304, "")
	first := get("/media/library?limit=1")
	expectAPI(t, first, 200, "")
	next, ok := first.json["next"].(string)
	if !ok || len(first.json["items"].([]any)) != 1 {
		t.Fatalf("first page: %s", first.body)
	}
	second := get("/media/library?limit=1&cursor=" + url.QueryEscape(next))
	expectAPI(t, second, 200, "")
	if len(second.json["items"].([]any)) != 1 || second.json["items"].([]any)[0].(map[string]any)["id"] == first.json["items"].([]any)[0].(map[string]any)["id"] {
		t.Fatalf("second page: %s", second.body)
	}
	expectAPI(t, get("/media/library?q=other&limit=1&cursor="+url.QueryEscape(next)), 400, "bad_cursor")
	expectAPI(t, get("/media/library?limit=101"), 422, "invalid")

	detail := get("/media/library/movie/" + mediaAPIID(libraryMovie))
	expectAPI(t, detail, 200, "")
	private(detail)
	files := detail.json["items"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["id"] != mediaAPIID(movie) {
		t.Fatalf("detail: %s", detail.body)
	}
	show := get("/media/library/show/" + mediaAPIID(libraryShow))
	expectAPI(t, show, 200, "")
	private(show)

	minted := time.Now()
	link := get("/media/files/" + mediaAPIID(movie) + "/link")
	expectAPI(t, link, 200, "")
	public, _ := link.json["public"].(string)
	vpn, _ := link.json["vpn"].(string)
	if !strings.HasPrefix(public, "https://atrium.test"+media.FilePrefix) || vpn != "https://artemis.vpn:8443/library/movies/Dune%20%282021%29/Dune%20%282021%29.mkv" || link.json["ttl_seconds"] != float64(60) {
		t.Fatalf("link: %s", link.body)
	}
	signed := strings.TrimPrefix(public, "https://atrium.test")
	expiry := func(signed string) int64 {
		t.Helper()
		exp, err := strconv.ParseInt(signed[strings.Index(signed, "exp=")+4:strings.Index(signed, "&sig=")], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return exp
	}
	exp := expiry(signed)
	if exp < minted.Unix()+59 || exp > time.Now().Unix()+61 {
		t.Fatalf("expiry %d for a 60 s link minted at %d", exp, minted.Unix())
	}
	if link.header.Get("ETag") != "" || link.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("cacheable link: %v", link.header)
	}
	time.Sleep(time.Until(time.Unix(exp-59, 0)))
	relinked := get("/media/files/"+mediaAPIID(movie)+"/link", "If-None-Match", detail.header.Get("ETag"))
	expectAPI(t, relinked, 200, "")
	fresh, _ := relinked.json["public"].(string)
	if relinked.header.Get("ETag") != "" || fresh == "" || expiry(fresh) <= exp {
		t.Fatalf("revalidated link: %v %s", relinked.header, relinked.body)
	}
	redeem := func(host string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, s.base+signed, nil)
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, got := redeem(""); code != http.StatusOK || got != body {
		t.Fatalf("redeem = %d %q", code, got)
	}
	if code, got := redeem("evil.test"); code != http.StatusBadRequest || got == body {
		t.Fatalf("redeem on a foreign Host = %d", code)
	}
	for _, id := range []string{
		mediaAPIID("movies/" + libraryMovie + "/Missing.mkv"),
		mediaAPIID("movies/" + libraryMovie + "/notes.txt"),
		mediaAPIID("movies/../downloads/secret.mkv"),
		mediaAPIID("downloads/secret.mkv"),
		mediaAPIID("movies/../../atrium.db"),
		"..%2F..%2Fatrium.db",
		"not+base64",
	} {
		r := get("/media/files/" + id + "/link")
		expectAPI(t, r, 404, "not_found")
		private(r)
	}
	for _, id := range []string{mediaAPIID(".."), mediaAPIID(libraryMovie + "/.."), mediaAPIID("Missing")} {
		expectAPI(t, get("/media/library/movie/"+id), 404, "not_found")
	}
	expectAPI(t, get("/media/library/downloads/"+mediaAPIID("secret.mkv")), 404, "not_found")

	arrival := "/media/filings/" + hash + "?kind=movie&title=Arrival&year=2016"
	preview := get(arrival)
	expectAPI(t, preview, 200, "")
	private(preview)
	planned, _ := preview.json["files"].([]any)
	if preview.json["ready"] != true || preview.json["kind"] != "movie" || len(planned) != 1 {
		t.Fatalf("filing preview: %s", preview.body)
	}
	if f := planned[0].(map[string]any); f["name"] != "Arrival.2016.mkv" || f["id"] != mediaAPIID("Arrival.2016.mkv") || f["destination"] != "movies/Arrival (2016)/Arrival (2016).mkv" {
		t.Fatalf("filing preview file: %s", preview.body)
	}
	expectAPI(t, get("/media/filings/"+hash+"?kind=tv&title=Arrival&year=2016"), 422, "invalid")
	expectAPI(t, get(arrival+"&episodes=bogus"), 422, "invalid")
	expectAPI(t, get(arrival+"&episodes="+mediaAPIID("Missing.mkv")+":1:1"), 422, "invalid")
	expectAPI(t, get("/media/filings/"+untagged+"?kind=movie&title=Heat&year=1995"), 404, "not_found")
	expectAPI(t, get("/media/filings/"+mediaAPIID("unknown")), 404, "not_found")
	fileIt := func(body any, headers ...string) apiReply {
		return s.do(http.MethodPost, "/api/v1/media/filings/"+hash, token, body, headers...)
	}
	for _, bad := range []struct {
		body  map[string]any
		field string
	}{
		{map[string]any{"kind": "movie", "title": "Arrival", "year": "2016"}, "action"},
		{map[string]any{"action": "file", "title": "Arrival", "year": "2016"}, "kind"},
		{map[string]any{"action": "file", "kind": "tv", "title": "Arrival", "year": "2016"}, "kind"},
		{map[string]any{"action": "preview", "kind": "movie", "title": "Arrival", "year": "2016"}, "action"},
		{map[string]any{"action": "file", "kind": "movie", "title": "Arrival", "year": "2016", "episodes": []any{map[string]any{"file": mediaAPIID("Missing.mkv"), "season": "1", "episode": "1"}}}, "episodes"},
	} {
		refused := fileIt(bad.body)
		expectAPI(t, refused, 422, "invalid")
		if !strings.Contains(refused.body, `"`+bad.field) || mediaEffectCount(t, d, media.OpFile, hash) != 0 {
			t.Fatalf("filing without a valid %s: %s", bad.field, refused.body)
		}
	}
	plan := map[string]any{"action": "file", "kind": "movie", "title": "Arrival", "year": "2016"}
	filed := fileIt(plan, "Idempotency-Key", "file-arrival")
	expectAPI(t, filed, 200, "")
	private(filed)
	if filed.json["placed"] != float64(1) {
		t.Fatalf("filed: %s", filed.body)
	}
	replayed := fileIt(plan, "Idempotency-Key", "file-arrival")
	expectAPI(t, replayed, 200, "")
	if replayed.body != filed.body || replayed.header.Get("Atrium-Command-Id") != filed.header.Get("Atrium-Command-Id") {
		t.Fatalf("replayed filing: %s", replayed.body)
	}
	expectAPI(t, fileIt(plan), 404, "not_found")
	expectAPI(t, get(arrival), 404, "not_found")
	if n := mediaEffectCount(t, d, media.OpFile, hash); n != 1 {
		t.Fatalf("filing effects = %d", n)
	}
	if _, err := os.Stat(filepath.Join(root, "movies", "Arrival (2016)", "Arrival (2016).mkv")); err != nil {
		t.Fatal(err)
	}

	post := func(path string) apiReply { return s.do(http.MethodPost, "/api/v1"+path, token, nil) }
	expectAPI(t, post("/media/filings/"+untagged+"/ignore"), 404, "not_found")
	expectAPI(t, post("/media/filings/"+skipped+"/ignore"), 200, "")
	expectAPI(t, get("/media/filings/"+skipped+"?kind=movie&title=Heat&year=1995"), 404, "not_found")
	expectAPI(t, post("/media/filings/"+skipped+"/ignore"), 404, "not_found")

	lock, err := db.ResourceLocks(context.Background(), filepath.Join(dir, "atrium.db"), []string{"media-control"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	expectAPI(t, post("/media/sweep"), 409, "conflict")
	if qb.count("delete") != 0 {
		t.Fatal("sweep ran while the media-control lock was held")
	}
	_ = lock.Close()
	swept := post("/media/sweep")
	expectAPI(t, swept, 200, "")
	if swept.json["removed"] != float64(1) || swept.json["errors"] != float64(0) || qb.count("delete") != 1 {
		t.Fatalf("sweep: %s", swept.body)
	}

	if err := os.RemoveAll(agent.roots["tv"]); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 60*time.Second, "the API catalogue to turn stale", func() bool {
		r := get("/media/library")
		if r.status != http.StatusOK {
			t.Fatalf("stale library: %s", r.body)
		}
		if r.json["catalogue"].(map[string]any)["stale"] != true {
			time.Sleep(time.Second)
			return false
		}
		return len(r.json["items"].([]any)) >= 2
	})
	time.Sleep(time.Until(time.Unix(exp+1, 0)))
	if code, got := redeem(""); code != http.StatusForbidden || got == body {
		t.Fatalf("expired link = %d", code)
	}
}

func TestMediaLibraryAPIWithoutCatalogueIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	_, qbURL := mnpNewQbit(t)
	agent, _ := agentLibrary(t, dir, qbURL)
	agent.terminate(t)
	seedAllFlagsIn(t, dir)
	token := mediaAPIToken(t)
	s := &apiSite{t: t, base: startServeIn(t, dir)}
	for _, path := range []string{"/media/library", "/media/library?q=dune", "/media/library/movie/" + mediaAPIID(libraryMovie), "/media/files/" + mediaAPIID("movies/"+libraryMovie+"/"+libraryMovie+".mkv") + "/link"} {
		expectAPI(t, s.do(http.MethodGet, "/api/v1"+path, token, nil), 503, "unavailable")
	}
}
