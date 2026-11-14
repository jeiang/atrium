package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
)

type mnpFile struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
}

type mnpTorrent struct {
	hash, name, state, content string
	tags                       []string
	progress                   float64
	files                      []mnpFile
}

type mnpQbit struct {
	mu      sync.Mutex
	order   []string
	items   map[string]*mnpTorrent
	added   []url.Values
	writes  []string
	deleted []string
	drop    map[string]bool
}

func mnpNewQbit(t *testing.T) (*mnpQbit, string) {
	t.Helper()
	f := &mnpQbit{items: map[string]*mnpTorrent{}, drop: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *mnpQbit) dropOnce(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drop[path] = true
}

func (f *mnpQbit) shouldDrop(path string) bool {
	if f.drop[path] {
		delete(f.drop, path)
		return true
	}
	return false
}

func (f *mnpQbit) count(what string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch what {
	case "add":
		return len(f.added)
	case "delete":
		return len(f.deleted)
	}
	n := 0
	for _, w := range f.writes {
		if strings.HasPrefix(w, what) {
			n++
		}
	}
	return n
}

func mnpDropConn(w http.ResponseWriter) {
	if hijacker, ok := w.(http.Hijacker); ok {
		if conn, _, err := hijacker.Hijack(); err == nil {
			_ = conn.Close()
		}
	}
}

func (f *mnpQbit) put(tor mnpTorrent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[tor.hash]; !ok {
		f.order = append(f.order, tor.hash)
	}
	f.items[tor.hash] = &tor
}

func (f *mnpQbit) state(hash string) (tags []string, priorities []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.items[hash]
	for _, file := range t.files {
		priorities = append(priorities, file.Priority)
	}
	return slices.Clone(t.tags), priorities
}

func (f *mnpQbit) touched(hash string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.writes {
		if strings.Contains(w, hash) {
			return true
		}
	}
	return false
}

func (f *mnpQbit) lastAdd() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.added) == 0 {
		return nil
	}
	return f.added[len(f.added)-1]
}

func mnpTags(s string) []string {
	var out []string
	for _, tag := range strings.Split(s, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

func (f *mnpQbit) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/v2/")
	q := r.URL.Query()
	switch path {
	case "app/version":
		if f.shouldDrop(path) {
			mnpDropConn(w)
			return
		}
		_, _ = io.WriteString(w, "v5.2.0")
		return
	case "torrents/info":
		rows := []map[string]any{}
		for _, h := range f.order {
			t := f.items[h]
			if q.Get("filter") == "completed" && t.progress < 1 {
				continue
			}
			if q.Get("tag") != "" && !slices.Contains(t.tags, q.Get("tag")) {
				continue
			}
			if q.Get("hashes") != "" && !slices.Contains(strings.Split(q.Get("hashes"), "|"), h) {
				continue
			}
			rows = append(rows, map[string]any{"hash": t.hash, "name": t.name, "tags": strings.Join(t.tags, ", "), "state": t.state,
				"content_path": t.content, "progress": t.progress, "size": 100, "dlspeed": 0, "upspeed": 0, "eta": 0})
		}
		if f.shouldDrop(path) {
			mnpDropConn(w)
			return
		}
		_ = json.NewEncoder(w).Encode(rows)
		return
	case "torrents/files":
		t, ok := f.items[q.Get("hash")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if f.shouldDrop(path) {
			mnpDropConn(w)
			return
		}
		_ = json.NewEncoder(w).Encode(t.files)
		return
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		_ = r.ParseMultipartForm(1 << 20)
	} else {
		_ = r.ParseForm()
	}
	form := r.Form
	f.writes = append(f.writes, path+" "+form.Get("hashes")+form.Get("hash"))
	drop := f.shouldDrop(path)
	switch path {
	case "torrents/add":
		f.added = append(f.added, form)
		if r.MultipartForm != nil {
			for _, header := range r.MultipartForm.File["torrents"] {
				file, err := header.Open()
				if err != nil {
					http.Error(w, "upload", 400)
					return
				}
				data, err := io.ReadAll(file)
				_ = file.Close()
				hash, hashErr := media.TorrentHash(data)
				if err != nil || hashErr != nil {
					http.Error(w, "torrent", 400)
					return
				}
				if _, ok := f.items[hash]; !ok {
					f.order = append(f.order, hash)
					f.items[hash] = &mnpTorrent{hash: hash, name: header.Filename, state: "metaDL", tags: mnpTags(form.Get("tags"))}
				}
			}
		}
		if hash, ok := media.MagnetHash(form.Get("urls")); ok && hash != "" {
			if _, ok := f.items[hash]; !ok {
				f.order = append(f.order, hash)
				f.items[hash] = &mnpTorrent{hash: hash, name: "magnet " + hash[:8], state: "metaDL", tags: mnpTags(form.Get("tags"))}
			}
		}
		if drop {
			mnpDropConn(w)
			return
		}
		_, _ = io.WriteString(w, `{"success_count":1}`)
	case "torrents/addTags", "torrents/removeTags":
		for _, h := range strings.Split(form.Get("hashes"), "|") {
			t, ok := f.items[h]
			if !ok {
				continue
			}
			for _, tag := range mnpTags(form.Get("tags")) {
				t.tags = slices.DeleteFunc(t.tags, func(existing string) bool { return existing == tag })
				if path == "torrents/addTags" {
					t.tags = append(t.tags, tag)
				}
			}
		}
	case "torrents/filePrio":
		t := f.items[form.Get("hash")]
		priority, _ := strconv.Atoi(form.Get("priority"))
		for _, id := range strings.Split(form.Get("id"), "|") {
			i, _ := strconv.Atoi(id)
			t.files[i].Priority = priority
		}
	case "torrents/start":
		for _, h := range strings.Split(form.Get("hashes"), "|") {
			if t, ok := f.items[h]; ok {
				t.state = "downloading"
			}
		}
	case "torrents/delete":
		for _, h := range strings.Split(form.Get("hashes"), "|") {
			if _, ok := f.items[h]; ok {
				f.deleted = append(f.deleted, h)
				delete(f.items, h)
				f.order = slices.DeleteFunc(f.order, func(existing string) bool { return existing == h })
			}
		}
	default:
		http.NotFound(w, r)
		return
	}
	if drop {
		mnpDropConn(w)
	}
}

type mnpBrowser struct {
	t      *testing.T
	base   string
	client *http.Client
	csrf   string
}

func mnpNewBrowser(t *testing.T, base string) *mnpBrowser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &mnpBrowser{t: t, base: base, client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *mnpBrowser) do(method, target string, form url.Values, headers ...string) (int, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, b.base+target, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range b.client.Jar.Cookies(req.URL) {
			if c.Name == "atrium_csrf" {
				req.Header.Set("X-CSRF-Token", c.Value)
			}
		}
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	text, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(text)
}

func (b *mnpBrowser) want(method, target string, form url.Values, status int, wants ...string) string {
	b.t.Helper()
	code, body := b.do(method, target, form)
	if code != status {
		b.t.Fatalf("%s %s = %d, want %d: %.600s", method, target, code, status, body)
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			b.t.Errorf("%s %s misses %q: %.600s", method, target, w, body)
		}
	}
	return body
}

func mnpWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mnpMediaRoot(t *testing.T, dir string) string {
	t.Helper()
	root := filepath.Join(dir, "media")
	for _, d := range []string{"import", "movies", "tv", "downloads"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestMnpMediaWebWritesOnlyTouchMediaTorrents(t *testing.T) {
	t.Parallel()
	const (
		ready = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		busy  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		plain = "cccccccccccccccccccccccccccccccccccccccc"
		fresh = "dddddddddddddddddddddddddddddddddddddddd"
		name  = "Lanterns.S01E08.1080p.WEB.h264-ETHEL"
	)
	dir := t.TempDir()
	qb, qbURL := mnpNewQbit(t)
	_, root := agentLibrary(t, dir, qbURL)

	content := filepath.Join(root, "downloads", name)
	mnpWrite(t, filepath.Join(content, name+".mkv"), make([]byte, 100))
	mnpWrite(t, filepath.Join(content, name+".en.srt"), make([]byte, 100))
	qb.put(mnpTorrent{hash: ready, name: name, tags: []string{media.TagMedia}, state: media.StoppedUP, progress: 1, content: content})
	qb.put(mnpTorrent{hash: busy, name: "Dune.2021", tags: []string{media.TagMedia}, state: "stoppedDL", content: "Dune.2021",
		files: []mnpFile{{0, "Dune.2021.mkv", 0}, {1, "Dune.2021.nfo", 1}}})
	qb.put(mnpTorrent{hash: plain, name: "plain.thing", state: "stoppedDL", content: "plain.thing",
		files: []mnpFile{{0, "c.mkv", 0}, {1, "c.txt", 1}}})

	seedAllFlagsIn(t, dir)
	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	b.want(http.MethodGet, "/apps/media/torrents", nil, http.StatusOK)
	if qb.touched(ready) || qb.touched(busy) || qb.touched(plain) {
		t.Fatalf("viewing downloads wrote to qBittorrent: %v", qb.writes)
	}

	b.want(http.MethodPost, "/apps/media/torrents/"+plain+"/files", url.Values{"index": {"0"}, "wanted": {"1"}}, http.StatusNotFound)
	b.want(http.MethodPost, "/apps/media/torrents/"+busy+"/files", url.Values{"index": {"1"}, "wanted": {"0"}}, http.StatusOK)
	if _, prio := qb.state(busy); !slices.Equal(prio, []int{0, 0}) {
		t.Fatalf("media torrent priorities %v, want [0 0]", prio)
	}

	magnet := "magnet:?xt=urn:btih:" + fresh + "&dn=Lanterns.S01E09"
	b.want(http.MethodPost, "/apps/media/add", url.Values{"magnet": {magnet}, "mode": {"media"}}, http.StatusOK)
	if got := qb.lastAdd(); got.Get("urls") != magnet || got.Get("tags") != media.TagMedia {
		t.Fatalf("media add sent %v", got)
	}
	b.want(http.MethodPost, "/apps/media/add", url.Values{"magnet": {"magnet:?xt=urn:btih:" + strings.Repeat("e", 40)}, "mode": {"plain"}}, http.StatusOK)
	if got := qb.lastAdd(); got.Get("tags") != "" {
		t.Fatalf("plain add carried tags %q", got.Get("tags"))
	}

	form := func(action string) url.Values {
		return url.Values{"id": {ready}, "action": {action}, "kind": {"tv"}, "title": {"Lanterns"}, "year": {"2026"}, "s0": {"1"}, "e0": {"8"}}
	}
	dest := filepath.Join(root, "tv", "Lanterns (2026)", "Season 01", "Lanterns (2026) - S01E08.mkv")
	b.want(http.MethodPost, "/apps/media/file", form("preview"), http.StatusOK)
	if _, err := os.Stat(filepath.Join(root, "tv", "Lanterns (2026)")); !os.IsNotExist(err) {
		t.Fatalf("preview touched the library: %v", err)
	}
	if tags, _ := qb.state(ready); slices.Contains(tags, media.TagFiled) {
		t.Fatal("preview tagged the torrent filed")
	}
	b.want(http.MethodPost, "/apps/media/file", form("file"), http.StatusOK)
	src, err := os.Stat(filepath.Join(content, name+".mkv"))
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.Stat(dest)
	if err != nil || !os.SameFile(src, dst) {
		t.Fatalf("filing did not hardlink the download into the library: %v", err)
	}
	if tags, _ := qb.state(ready); !slices.Contains(tags, media.TagFiled) {
		t.Fatalf("filed torrent tags %v", tags)
	}

	if tags, prio := qb.state(plain); len(tags) != 0 || !slices.Equal(prio, []int{0, 1}) || qb.touched(plain) {
		t.Fatalf("the untagged torrent was changed: tags %v priorities %v writes %v", tags, prio, qb.writes)
	}
}

func TestMnpMediaChannelFirstPollPrimesWithoutDownloading(t *testing.T) {
	t.Parallel()
	const (
		old    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		latest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	releases := []map[string]any{
		{"hash": old, "title": "Lanterns S01E01 1080p WEB", "magnet_url": "magnet:?xt=urn:btih:" + old + "&dn=x", "size_bytes": "1234567890", "season": "1", "episode": "1"},
		{"hash": latest, "title": "lanterns s01e01 720p", "magnet_url": "magnet:?xt=urn:btih:" + latest + "&dn=x", "size_bytes": "1234567890", "season": "1", "episode": "1"},
	}
	eztv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list := []map[string]any{}
		if r.URL.Query().Get("imdb_id") == "1234567" {
			list = releases
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"torrents": list})
	}))
	t.Cleanup(eztv.Close)

	dir := t.TempDir()
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, eztv.URL+"/api/get-torrents", "", ""))

	seedAllFlagsIn(t, dir)
	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/channels", nil, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/channels", url.Values{"name": {"Lanterns"}, "imdb": {"tt1234567"}, "pattern": {"1080p"}}, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/channels", url.Values{"name": {"Lanterns"}, "imdb": {"nope"}, "pattern": {"1080p"}}, http.StatusUnprocessableEntity)
	b.want(http.MethodPost, "/apps/media/channels/poll", url.Values{}, http.StatusOK)
	if qb.lastAdd() != nil {
		t.Fatalf("the first poll downloaded %v", qb.added)
	}

	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	seen := func(hash string) bool {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM media_seen WHERE hash = ?", hash).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}
	if !seen(old) || seen(latest) {
		t.Fatalf("first poll seen: matching %v, other %v; want only the matching release", seen(old), seen(latest))
	}

	b.want(http.MethodPost, "/apps/media/channels", url.Values{"name": {"Lanterns B"}, "imdb": {"tt1234567"}, "pattern": {"720p"}, "backfill": {"1"}}, http.StatusOK)
	if len(qb.added) != 1 || qb.added[0].Get("tags") != media.TagMedia || !strings.Contains(qb.added[0].Get("urls"), latest) {
		t.Fatalf("backfill adds = %v", qb.added)
	}
}

func mnpSign(key, rel string, exp int64) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(rel + "\n" + strconv.FormatInt(exp, 10)))
	return fmt.Sprintf("%s%s?exp=%d&sig=%s", media.FilePrefix, mnpQuote(rel), exp, hex.EncodeToString(mac.Sum(nil)))
}

func mnpQuote(s string) string {
	return strings.ReplaceAll(url.PathEscape(s), "%2F", "/")
}

func mnpFlipLast(s string) string {
	if strings.HasSuffix(s, "0") {
		return s[:len(s)-1] + "1"
	}
	return s[:len(s)-1] + "0"
}

func TestMnpMediaSignedLinksAdmitOnlyValidSignaturesOnAllowedHosts(t *testing.T) {
	t.Parallel()
	const (
		key   = "mnp-link-key"
		far   = int64(4_000_000_000)
		movie = "movies/Dune (2021)/Dune (2021).mkv"
		group = "atrium_access"
	)
	dir := t.TempDir()
	mnpMediaRoot(t, dir)
	agent := startMediaAgent(t, mediaAgentOpts{main: true, dir: dir})
	root := filepath.Dir(agent.roots["movies"])
	body := strings.Repeat("abcdefghij", 10)
	mnpWrite(t, filepath.Join(root, movie), []byte(body))
	mnpWrite(t, filepath.Join(root, "downloads", "secret.mkv"), []byte("do not serve"))
	setEnv(t, settingEnv(config.KeyPublicURL), "https://atrium.test")
	setEnv(t, settingEnv(media.KeyVPNBaseURL), "https://artemis.vpn:8443")
	keyFile := filepath.Join(dir, "link.key")
	if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "host-appkeys.toml", "[app_keys]\nlink_key_file = "+strconv.Quote(keyFile)+"\n"+defaultQbitConnection+agentConnectionTOML(agent.base, filepath.Join(agent.dir, "token"))))
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	seedAllFlagsIn(t, dir)
	base, _, stop, err := startServeAppArgs(t, newTestApp(t), &lockedBuffer{}, "--auth", "proxy", "--allowed-host", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	b := mnpNewBrowser(t, base)
	expect := func(label, target string, status int, headers ...string) string {
		t.Helper()
		code, text := b.do(http.MethodGet, target, nil, headers...)
		if code != status {
			t.Errorf("%s: GET %s = %d, want %d: %.300s", label, target, code, status, text)
		}
		if strings.Contains(text, "do not serve") {
			t.Errorf("%s: served a file outside the library", label)
		}
		return text
	}

	page := expect("movie page with the group", "/apps/media/library/movie/Dune%20%282021%29", http.StatusOK, "Remote-Groups", group)
	i := strings.Index(page, `href="https://atrium.test`+media.FilePrefix)
	if i < 0 {
		t.Fatalf("no signed public link on the movie page: %.600s", page)
	}
	rest := page[i+len(`href="https://atrium.test`):]
	good := strings.ReplaceAll(rest[:strings.Index(rest, `"`)], "&amp;", "&")

	if got := expect("signed link without the group", good, http.StatusOK); got != body {
		t.Errorf("signed link body %q", got)
	}
	if got := expect("signed range without the group", good, http.StatusPartialContent, "Range", "bytes=10-19"); got != body[10:20] {
		t.Errorf("range body %q, want %q", got, body[10:20])
	}
	expect("unsatisfiable signed range", good, http.StatusRequestedRangeNotSatisfiable, "Range", "bytes=100-")
	expect("signed link on a foreign host", good, http.StatusBadRequest, "Host", "evil.test")
	expect("page without the group", "/apps/media/downloads", http.StatusForbidden)
	expect("tampered signature", mnpFlipLast(good), http.StatusForbidden)
	expect("tampered signature with a range", mnpFlipLast(good), http.StatusForbidden, "Range", "bytes=10-19")
	exp := good[strings.Index(good, "exp=")+4 : strings.Index(good, "&sig=")]
	expect("tampered expiry", strings.Replace(good, "exp="+exp, "exp="+exp+"0", 1), http.StatusForbidden)
	expect("unsigned", media.FilePrefix+mnpQuote(movie), http.StatusForbidden)
	expect("unsigned with the group", media.FilePrefix+mnpQuote(movie), http.StatusForbidden, "Remote-Groups", group)
	expect("expired", mnpSign(key, movie, 1_000_000_000), http.StatusForbidden)
	expect("another key", mnpSign("other", movie, far), http.StatusForbidden)
	expect("pinned key", mnpSign(key, movie, far), http.StatusOK)
	expect("traversal into downloads", mnpSign(key, "movies/../downloads/secret.mkv", far), http.StatusNotFound)
	expect("outside movies and tv", mnpSign(key, "downloads/secret.mkv", far), http.StatusNotFound)
	expect("parent of the root", mnpSign(key, "movies/../../atrium.db", far), http.StatusNotFound)
	sig := mnpSign(key, movie, far)
	expect("signature on another route", "/apps/media/thumb/"+mnpQuote(movie)+sig[strings.Index(sig, "?"):], http.StatusForbidden)
}

func TestMediaAddRefusesNonV1MagnetEverywhere(t *testing.T) {
	v2only := "magnet:?xt=urn:btmh:1220" + strings.Repeat("a", 64) + "&dn=Lanterns.S01E12"

	dir := t.TempDir()
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, "", "", ""))
	seedAllFlagsIn(t, dir)
	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/add", url.Values{"magnet": {v2only}, "mode": {"media"}}, http.StatusUnprocessableEntity)
	b.want(http.MethodPost, "/apps/media/add", url.Values{
		"magnet":  {mediaMagnet(strings.Repeat("a", 40), "Lanterns")},
		"torrent": {"ZDE6YWJl"}, "name": {"lanterns.torrent"}, "mode": {"media"},
	}, http.StatusUnprocessableEntity)
	b.want(http.MethodPost, "/apps/media/add", url.Values{
		"torrent": {base64.StdEncoding.EncodeToString([]byte("d4:infod9:file treede12:meta versioni2e4:name8:test.mkv12:piece lengthi16384eee"))},
		"name":    {"lanterns.torrent"}, "mode": {"media"},
	}, http.StatusUnprocessableEntity)
	webDB := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = webDB.Close() }()
	var effects, invalid int
	if err := webDB.Read.QueryRow("SELECT COUNT(*) FROM effects").Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if err := webDB.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = 'media.add' AND outcome = 'invalid'").Scan(&invalid); err != nil {
		t.Fatal(err)
	}
	if effects != 0 || invalid != 3 {
		t.Fatalf("web refusals: %d effects, %d invalid audits; want 0, 3", effects, invalid)
	}
	if adds := qb.count("add"); adds != 0 {
		t.Fatalf("the refused web add reached qBittorrent: %d adds", adds)
	}

	toolDir, _, _ := moduleFixture(t, flagsExcept("", ""))
	mnpMediaRoot(t, toolDir)
	toolQB, toolURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, toolDir, toolURL, "", "", ""))
	result := unescape(toolResult(t, "media_add_magnet", map[string]any{"magnet": v2only}))
	if !strings.Contains(result, `"code":"invalid"`) || !strings.Contains(result, "magnet") {
		t.Fatalf("media_add_magnet with a v2-only magnet = %s, want an invalid tool error", result)
	}
	if adds := toolQB.count("add"); adds != 0 {
		t.Fatalf("the refused tool add reached qBittorrent: %d adds", adds)
	}
	d := mustConnect(t, filepath.Join(toolDir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	var cards int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM proposals WHERE kind = 'media_add_magnet'").Scan(&cards); err != nil {
		t.Fatal(err)
	}
	if cards != 0 {
		t.Fatalf("the refused tool add created %d approval cards", cards)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effects").Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = 'media.add' AND outcome = 'invalid'").Scan(&invalid); err != nil {
		t.Fatal(err)
	}
	if effects != 0 || invalid != 1 {
		t.Fatalf("tool refusal: %d effects, %d invalid audits; want 0, 1", effects, invalid)
	}
}
