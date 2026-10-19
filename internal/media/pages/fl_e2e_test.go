package pages

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	flFar      = 4_000_000_000
	flKey      = "secret-key"
	flJPEGTail = "\xff\xd9"
	flDenied   = `{"detail":"the link is invalid or has expired"}`
	flGroup    = "atrium_access"
)

var (
	flJPEG        = []byte("\xff\xd8\xff\xdb" + strings.Repeat("\x00", 16) + flJPEGTail)
	flPlaceholder = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 2 3"><rect width="2" height="3" fill="#80808033"/></svg>`
	flMovieBody   = flBody()
)

func flBody() []byte {
	out := make([]byte, 100)
	for i := range out {
		out[i] = byte('a' + i%26)
	}
	return out
}

type flWorld struct {
	*site
	root       string
	movieRel   string
	episodeRel string
}

func flNew(t *testing.T, vars map[string]string, o Options) *flWorld {
	t.Helper()
	root := filepath.Join(t.TempDir(), "media")
	for _, d := range []string{"import", "movies", "tv", "downloads"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	movieRel := "movies/Dune (2021)/Dune (2021).mkv"
	episodeRel := "tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E08.mkv"
	flWrite(t, filepath.Join(root, movieRel), flMovieBody)
	flWrite(t, filepath.Join(root, episodeRel), []byte("episode-bytes"))
	all := map[string]string{
		"ATRIUM_MEDIA_ROOT":       root,
		"ATRIUM_PUBLIC_URL":       "https://atrium.test",
		"ATRIUM_MEDIA_MOVIES_URL": "http://artemis.vpn:8084",
		"ATRIUM_MEDIA_TV_URL":     "http://artemis.vpn:8085",
		"TMDB_API_KEY":            "k",
		"ATRIUM_MEDIA_LINK_KEY":   flKey,
	}
	for k, v := range vars {
		all[k] = v
	}
	return &flWorld{site: newSite(t, all, o), root: root, movieRel: movieRel, episodeRel: episodeRel}
}

func flWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func flSigned(key, rel string, exp int64) string {
	return fmt.Sprintf("%s%s?exp=%d&sig=%s", media.FilePrefix, py.URLQuote(rel), exp, media.Sign([]byte(key), rel, exp))
}

func flRange(spec string) [2]string { return [2]string{"Range", spec} }

func flProxy(t *testing.T, s *site) *site {
	t.Helper()
	render, err := web.NewRenderer(s.cfg, s.db, tpl.Config{Modules: []fs.FS{Templates()}})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := web.New(web.Options{
		Cfg: s.cfg, Env: s.env, DB: s.db, Auth: web.AuthProxy, AllowedHosts: []string{host}, Public: PublicCheck(s.cfg, s.env),
		Render: render, Modules: []web.Module{Module(Options{})},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &site{t: t, db: s.db, cfg: s.cfg, env: s.env, dir: s.dir, srv: srv}
}

func flGetHost(s *site, target, requestHost string, headers ...[2]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", target, nil)
	req.Host = requestHost
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	rec := httptest.NewRecorder()
	s.srv.ServeHTTP(rec, req)
	return rec
}

func flBadLast(target string) string {
	last := target[len(target)-1]
	if last == '0' {
		return target[:len(target)-1] + "1"
	}
	return target[:len(target)-1] + "0"
}

func TestFlSignedFileChecklist(t *testing.T) {
	w := flNew(t, nil, Options{})
	good := flSigned(flKey, w.movieRel, flFar)

	t.Run("valid", func(t *testing.T) {
		rec := w.get(good)
		body := status(t, rec, 200, "valid link")
		if body != string(flMovieBody) {
			t.Fatalf("body %q", body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "video/x-matroska" {
			t.Errorf("content type %q", ct)
		}
		if ar := rec.Header().Get("Accept-Ranges"); ar != "bytes" {
			t.Errorf("accept-ranges %q", ar)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		cases := map[string]string{
			"tampered signature": flBadLast(good),
			"tampered expiry":    strings.Replace(good, fmt.Sprint(flFar), fmt.Sprint(flFar+1), 1),
			"expired":            flSigned(flKey, w.movieRel, 1_000_000_000),
			"unsigned":           media.FilePrefix + py.URLQuote(w.movieRel),
			"wrong path":         strings.Replace(good, "Dune%20%282021%29.mkv", "Dune%20%282021%29.mkvx", 1),
			"non-numeric expiry": media.FilePrefix + py.URLQuote(w.movieRel) + "?exp=soon&sig=00",
			"empty signature":    media.FilePrefix + py.URLQuote(w.movieRel) + "?exp=4000000000&sig=",
		}
		for name, target := range cases {
			rec := w.get(target)
			if got := status(t, rec, 403, name); got != flDenied {
				t.Errorf("%s: body %q", name, got)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("%s: content type %q", name, ct)
			}
		}
	})

	t.Run("last query value decides", func(t *testing.T) {
		status(t, w.get(good+"&exp=1"), 403, "later expired exp wins")
	})

	t.Run("missing file", func(t *testing.T) {
		status(t, w.get(flSigned(flKey, "movies/Nope/Nope.mkv", flFar)), 404, "missing")
	})

	t.Run("content types", func(t *testing.T) {
		want := map[string]string{
			"a.mkv": "video/x-matroska", "b.MP4": "video/mp4", "c.m4v": "video/x-m4v", "d.avi": "video/x-msvideo",
			"e.mov": "video/quicktime", "f.ts": "video/mp2t", "g.wmv": "video/x-ms-wmv", "h.srt": "application/octet-stream",
			"i": "application/octet-stream",
		}
		for name, kind := range want {
			rel := "movies/Types/" + name
			flWrite(t, filepath.Join(w.root, rel), []byte("x"))
			rec := w.get(flSigned(flKey, rel, flFar))
			status(t, rec, 200, name)
			if ct := rec.Header().Get("Content-Type"); ct != kind {
				t.Errorf("%s: content type %q, want %q", name, ct, kind)
			}
		}
	})
}

func TestFlRangeRequests(t *testing.T) {
	w := flNew(t, nil, Options{})
	good := flSigned(flKey, w.movieRel, flFar)

	t.Run("single", func(t *testing.T) {
		rec := w.get(good, flRange("bytes=10-19"))
		body := status(t, rec, 206, "single range")
		if body != string(flMovieBody[10:20]) || rec.Header().Get("Content-Range") != "bytes 10-19/100" {
			t.Fatalf("body %q range %q", body, rec.Header().Get("Content-Range"))
		}
	})

	t.Run("open ended", func(t *testing.T) {
		rec := w.get(good, flRange("bytes=95-"))
		body := status(t, rec, 206, "open range")
		if body != string(flMovieBody[95:]) || rec.Header().Get("Content-Range") != "bytes 95-99/100" {
			t.Fatalf("body %q range %q", body, rec.Header().Get("Content-Range"))
		}
	})

	t.Run("suffix", func(t *testing.T) {
		rec := w.get(good, flRange("bytes=-10"))
		body := status(t, rec, 206, "suffix range")
		if body != string(flMovieBody[90:]) || rec.Header().Get("Content-Range") != "bytes 90-99/100" {
			t.Fatalf("body %q range %q", body, rec.Header().Get("Content-Range"))
		}
	})

	t.Run("invalid", func(t *testing.T) {
		for _, spec := range []string{"bytes=abc", "items=0-9", "bytes=20-10"} {
			status(t, w.get(good, flRange(spec)), 416, spec)
		}
	})

	t.Run("unsatisfiable", func(t *testing.T) {
		rec := w.get(good, flRange("bytes=500-600"))
		status(t, rec, 416, "unsatisfiable")
		if got := rec.Header().Get("Content-Range"); got != "bytes */100" {
			t.Errorf("content-range %q", got)
		}
	})

	t.Run("multiple", func(t *testing.T) {
		rec := w.get(good, flRange("bytes=0-9,20-29"))
		status(t, rec, 206, "multiple ranges")
		kind, params, err := mime.ParseMediaType(rec.Header().Get("Content-Type"))
		if err != nil || kind != "multipart/byteranges" {
			t.Fatalf("content type %q: %v", rec.Header().Get("Content-Type"), err)
		}
		reader := multipart.NewReader(rec.Body, params["boundary"])
		wants := []struct{ rng, body string }{{"bytes 0-9/100", string(flMovieBody[0:10])}, {"bytes 20-29/100", string(flMovieBody[20:30])}}
		for _, want := range wants {
			part, err := reader.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(part)
			if part.Header.Get("Content-Range") != want.rng || string(data) != want.body {
				t.Errorf("part %q %q, want %q %q", part.Header.Get("Content-Range"), data, want.rng, want.body)
			}
		}
		if _, err := reader.NextPart(); err != io.EOF {
			t.Errorf("extra part: %v", err)
		}
	})

	t.Run("range does not bypass the signature", func(t *testing.T) {
		status(t, w.get(flBadLast(good), flRange("bytes=10-19")), 403, "tampered range request")
	})
}

func TestFlPathTraversalAndRoot(t *testing.T) {
	w := flNew(t, nil, Options{})
	flWrite(t, filepath.Join(w.root, "downloads", "secret.mkv"), []byte("do not serve"))
	flWrite(t, filepath.Join(filepath.Dir(w.root), "atrium.db"), []byte("database"))
	signedPath := func(rel string) string { return flSigned(flKey, rel, flFar) }
	rawPath := func(rel, rawRel string) string {
		return fmt.Sprintf("%s%s?exp=%d&sig=%s", media.FilePrefix, rawRel, flFar, media.Sign([]byte(flKey), rel, flFar))
	}
	cases := map[string]string{
		"parent traversal":         signedPath("movies/../../atrium.db"),
		"traversal to a real file": signedPath("movies/../downloads/secret.mkv"),
		"encoded traversal":        rawPath("movies/../../atrium.db", "movies/%2e%2e/%2e%2e/atrium.db"),
		"encoded dots only":        rawPath("../atrium.db", "%2e%2e/atrium.db"),
		"absolute":                 signedPath("/etc/passwd"),
		"nul byte":                 rawPath("movies/a\x00b", "movies/a%00b"),
		"outside movies and tv":    signedPath("downloads/secret.mkv"),
		"root itself":              signedPath("movies"),
		"directory":                signedPath("movies/Dune (2021)"),
		"empty":                    signedPath(""),
	}
	for name, target := range cases {
		rec := w.get(target)
		body := status(t, rec, 404, name)
		lacks(t, body, "do not serve", "database")
	}

	t.Run("dot components resolve like pathlib", func(t *testing.T) {
		status(t, w.get(signedPath("movies/./Dune (2021)//Dune (2021).mkv")), 200, "dot components")
	})

	t.Run("root not configured", func(t *testing.T) {
		bare := flNew(t, map[string]string{"ATRIUM_MEDIA_ROOT": ""}, Options{})
		status(t, bare.get(flSigned(flKey, bare.movieRel, flFar)), 404, "no root")
		status(t, bare.get(flSigned("wrong", bare.movieRel, flFar)), 403, "no root, bad signature")
	})
}

func TestFlRotatedAndGeneratedKeys(t *testing.T) {
	old := flNew(t, nil, Options{})
	oldLink := flSigned(flKey, old.movieRel, flFar)
	status(t, old.get(oldLink), 200, "current key")

	rotated := flNew(t, map[string]string{"ATRIUM_MEDIA_LINK_KEY": "rotated-key"}, Options{})
	status(t, rotated.get(oldLink), 403, "link from the previous key")
	status(t, rotated.get(flSigned("rotated-key", rotated.movieRel, flFar)), 200, "link from the new key")

	t.Run("key file created once without env", func(t *testing.T) {
		w := flNew(t, map[string]string{"ATRIUM_MEDIA_LINK_KEY": ""}, Options{})
		keyPath := w.cfg.MediaLinkKeyPath()
		if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
			t.Fatalf("key exists before first use: %v", err)
		}
		links := media.NewLinks(config.Media(w.env), keyPath)
		now := float64(time.Now().Unix())
		url, err := links.PublicURL(w.movieRel, now)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("key mode %v", info.Mode().Perm())
		}
		first, _ := os.ReadFile(keyPath)
		if len(first) != 64 {
			t.Fatalf("key length %d", len(first))
		}
		path := strings.TrimPrefix(url, "https://atrium.test")
		status(t, w.get(path), 200, "link signed with the generated key")
		again, err := media.NewLinks(config.Media(w.env), keyPath).PublicURL(w.movieRel, now)
		if err != nil || again != url {
			t.Fatalf("second Links built %q (%v), want %q", again, err, url)
		}
		if after, _ := os.ReadFile(keyPath); !bytes.Equal(after, first) {
			t.Errorf("key file rewritten")
		}

		req := httptest.NewRequest("GET", path, nil)
		if !PublicCheck(w.cfg, w.env)(req) {
			t.Fatal("guard hook rejects the link before rotation")
		}
		if err := os.Remove(keyPath); err != nil {
			t.Fatal(err)
		}
		if PublicCheck(w.cfg, w.env)(req) {
			t.Error("guard hook accepts the link after the key file was deleted")
		}
		if fresh, _ := os.ReadFile(keyPath); len(fresh) != 64 || bytes.Equal(fresh, first) {
			t.Errorf("rotation did not write a new key")
		}
	})
}

func TestFlGuardAdmitsSignedLinksOnly(t *testing.T) {
	w := flNew(t, nil, Options{})
	proxy := flProxy(t, w.site)
	good := flSigned(flKey, w.movieRel, flFar)
	bare := media.FilePrefix + py.URLQuote(w.movieRel)
	group := [2]string{"Remote-Groups", flGroup}

	status(t, flGetHost(proxy, good, host), 200, "signed link without group")
	status(t, flGetHost(proxy, good, "evil.test"), 400, "signed link, bad host")
	status(t, flGetHost(proxy, strings.Replace(good, fmt.Sprint(flFar), fmt.Sprint(flFar-1), 1), host), 403, "tampered expiry")
	status(t, flGetHost(proxy, bare, host), 403, "unsigned")
	status(t, flGetHost(proxy, flSigned(flKey, w.movieRel, 1_000_000_000), host), 403, "expired")
	status(t, flGetHost(proxy, flSigned("other", w.movieRel, flFar), host), 403, "wrong key")
	status(t, flGetHost(proxy, "/media/downloads", host), 403, "page without group")
	status(t, flGetHost(proxy, "/media/downloads", host, group), 200, "page with group")
	status(t, flGetHost(proxy, good, host, group), 200, "signed link with group")
	status(t, flGetHost(proxy, bare, host, group), 403, "unsigned link with group is still checked by the route")
	thumb := "/media/thumb/" + py.URLQuote(w.episodeRel)
	sig := media.Sign([]byte(flKey), w.episodeRel, flFar)
	status(t, flGetHost(proxy, fmt.Sprintf("%s?exp=%d&sig=%s", thumb, flFar, sig), host), 403, "signature does not unlock other paths")
	status(t, flGetHost(proxy, flSigned(flKey, w.movieRel, flFar), host, flRange("bytes=10-19")), 206, "range through the guard")
}

type flFFmpeg struct {
	mu       sync.Mutex
	calls    [][]string
	fail     bool
	duration string
	running  atomic.Int32
	peak     atomic.Int32
	delay    time.Duration
}

func (f *flFFmpeg) run(_ context.Context, argv []string) (string, string, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), argv...))
	f.mu.Unlock()
	now := f.running.Add(1)
	for {
		peak := f.peak.Load()
		if now <= peak || f.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	time.Sleep(f.delay)
	f.running.Add(-1)
	if argv[3] == "ffprobe" {
		return f.duration + "\n", "", 0, nil
	}
	if f.fail {
		return "", "boom", 1, nil
	}
	if err := os.WriteFile(argv[len(argv)-1], flJPEG, 0o644); err != nil {
		return "", err.Error(), 1, nil
	}
	return "", "", 0, nil
}

func (f *flFFmpeg) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type flTMDB struct {
	mu     sync.Mutex
	images int
	hits   int
}

func flServeTMDB(t *testing.T) *flTMDB {
	t.Helper()
	fake := &flTMDB{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.hits++
		if strings.HasPrefix(r.URL.Path, "/t/p/") {
			fake.images++
			fake.mu.Unlock()
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(flJPEG)
			return
		}
		fake.mu.Unlock()
		q := r.URL.Query()
		if q.Get("api_key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch path := strings.TrimPrefix(r.URL.Path, "/3/"); path {
		case "search/movie":
			results := `[]`
			if strings.Contains(strings.ToLower("Dune"), strings.ToLower(q.Get("query"))) && (q.Get("year") == "" || q.Get("year") == "2021") {
				results = `[{"title":"Dune","release_date":"2021-10-22","poster_path":"/dune.jpg"}]`
			}
			_, _ = io.WriteString(w, `{"results":`+results+`}`)
		case "search/tv":
			results := `[]`
			if strings.Contains(strings.ToLower("Lanterns"), strings.ToLower(q.Get("query"))) {
				results = `[{"id":42,"name":"Lanterns","first_air_date":"2026-01-01","poster_path":"/lant.jpg"}]`
			}
			_, _ = io.WriteString(w, `{"results":`+results+`}`)
		case "tv/42/season/1":
			_, _ = io.WriteString(w, `{"poster_path":"/s1.jpg"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	api, images := media.TMDBAPI, media.TMDBImages
	media.TMDBAPI, media.TMDBImages = srv.URL+"/3", srv.URL+"/t/p/w342"
	t.Cleanup(func() {
		media.TMDBAPI, media.TMDBImages = api, images
		srv.Close()
	})
	return fake
}

func (f *flTMDB) imageCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images
}

func (f *flTMDB) hitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits
}

func flImage(t *testing.T, rec *httptest.ResponseRecorder, label string) {
	t.Helper()
	status(t, rec, 200, label)
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("%s: content type %q", label, ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, max-age=86400" {
		t.Errorf("%s: cache control %q", label, cc)
	}
	if !bytes.Equal(rec.Body.Bytes(), flJPEG) {
		t.Errorf("%s: body is not the image", label)
	}
}

func flPlaceholderCheck(t *testing.T, rec *httptest.ResponseRecorder, label string) {
	t.Helper()
	body := status(t, rec, 200, label)
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("%s: content type %q", label, ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, max-age=600" {
		t.Errorf("%s: cache control %q", label, cc)
	}
	if body != flPlaceholder {
		t.Errorf("%s: body %q", label, body)
	}
}

func TestFlPostersAndThumbnails(t *testing.T) {
	tmdb := flServeTMDB(t)
	ff := &flFFmpeg{duration: "1200.5"}
	w := flNew(t, nil, Options{Exec: ff.run})

	flImage(t, w.get("/media/art/movie/"+py.URLQuote("Dune (2021)")), "movie poster")
	flImage(t, w.get("/media/art/show/"+py.URLQuote("Lanterns (2026)")), "show poster")
	flImage(t, w.get("/media/art/season/"+py.URLQuote("Lanterns (2026)")+"/1"), "season poster")
	if got := tmdb.imageCount(); got != 3 {
		t.Fatalf("%d image fetches, want 3", got)
	}
	flImage(t, w.get("/media/art/movie/"+py.URLQuote("Dune (2021)")), "cached poster")
	if got := tmdb.imageCount(); got != 3 {
		t.Fatalf("cached poster fetched again: %d", got)
	}
	cached := filepath.Join(w.cfg.MediaCacheDir(), "posters", "movie", "Dune (2021).jpg")
	if _, err := os.Stat(cached); err != nil {
		t.Errorf("poster not cached: %v", err)
	}

	flPlaceholderCheck(t, w.get("/media/art/movie/"+py.URLQuote("Unknown (1999)")), "unknown movie")
	if _, err := os.Stat(filepath.Join(w.cfg.MediaCacheDir(), "posters", "movie", "Unknown (1999).none")); err != nil {
		t.Errorf("miss not remembered: %v", err)
	}
	hits := tmdb.hitCount()
	flPlaceholderCheck(t, w.get("/media/art/movie/"+py.URLQuote("Unknown (1999)")), "remembered miss")
	if tmdb.hitCount() != hits {
		t.Errorf("remembered miss looked up again")
	}
	flPlaceholderCheck(t, w.get("/media/art/season/"+py.URLQuote("Lanterns (2026)")+"/2"), "season without poster")

	status(t, w.get("/media/art/bogus/x"), 404, "unknown kind")
	status(t, w.get("/media/art/season/x"), 404, "season with one segment")
	status(t, w.get("/media/art/movie/a%2Fb"), 404, "encoded slash in the name")
	rec := w.get("/media/art/season/Lanterns/one")
	body := status(t, rec, 422, "non-integer season")
	contains(t, body, `"int_parsing"`, `"number"`)

	flImage(t, w.get("/media/thumb/"+py.URLQuote(w.episodeRel)), "thumbnail")
	if len(ff.calls) != 2 {
		t.Fatalf("%d ffmpeg calls, want 2", len(ff.calls))
	}
	if got := strings.Join(ff.calls[0][:5], " "); got != "nice -n 10 ffprobe -v" {
		t.Errorf("probe call %q", got)
	}
	if ff.calls[1][3] != "ffmpeg" || !flHas(ff.calls[1], "120.05") {
		t.Errorf("ffmpeg call %v", ff.calls[1])
	}
	flImage(t, w.get("/media/thumb/"+py.URLQuote(w.episodeRel)), "cached thumbnail")
	if ff.count() != 2 {
		t.Errorf("cached thumbnail ran ffmpeg again: %d calls", ff.count())
	}
	if _, err := os.Stat(filepath.Join(w.cfg.MediaCacheDir(), "thumbs", w.episodeRel+".jpg")); err != nil {
		t.Errorf("thumbnail not cached: %v", err)
	}
	flPlaceholderCheck(t, w.get("/media/thumb/movies/Dune%20%282021%29/missing.mkv"), "missing thumbnail source")
	flPlaceholderCheck(t, w.get("/media/thumb/movies/../../atrium.db"), "traversal")
	flPlaceholderCheck(t, w.get("/media/thumb/"), "empty thumbnail path")
	if ff.count() != 2 {
		t.Errorf("invalid thumbnail paths ran ffmpeg: %d calls", ff.count())
	}
}

func flHas(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestFlThumbnailFailureIsRemembered(t *testing.T) {
	ff := &flFFmpeg{duration: "1200.5", fail: true}
	w := flNew(t, nil, Options{Exec: ff.run})
	target := "/media/thumb/" + py.URLQuote(w.episodeRel)
	flPlaceholderCheck(t, w.get(target), "failed ffmpeg")
	if ff.count() != 2 {
		t.Fatalf("%d calls, want probe and ffmpeg", ff.count())
	}
	marker := filepath.Join(w.cfg.MediaCacheDir(), "thumbs", w.episodeRel+".none")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("failure not remembered: %v", err)
	}
	flPlaceholderCheck(t, w.get(target), "remembered failure")
	if ff.count() != 2 {
		t.Errorf("remembered failure ran ffmpeg again: %d calls", ff.count())
	}
}

func TestFlThumbnailsRunOneAtATime(t *testing.T) {
	ff := &flFFmpeg{duration: "100", delay: 5 * time.Millisecond}
	w := flNew(t, nil, Options{Exec: ff.run})
	rels := make([]string, 6)
	for i := range rels {
		rels[i] = fmt.Sprintf("tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E%02d.mkv", i+20)
		flWrite(t, filepath.Join(w.root, rels[i]), []byte("x"))
	}
	var wg sync.WaitGroup
	recs := make([]*httptest.ResponseRecorder, len(rels))
	for i, rel := range rels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs[i] = w.get("/media/thumb/" + py.URLQuote(rel))
		}()
	}
	wg.Wait()
	for i, rec := range recs {
		flImage(t, rec, rels[i])
	}
	if got := ff.peak.Load(); got != 1 {
		t.Errorf("%d ffprobe or ffmpeg runs at once, want 1", got)
	}
	if got := ff.count(); got != 2*len(rels) {
		t.Errorf("%d calls, want %d", got, 2*len(rels))
	}
}

func TestFlWithoutTMDBOrRoot(t *testing.T) {
	tmdb := flServeTMDB(t)
	ff := &flFFmpeg{duration: "10"}
	w := flNew(t, map[string]string{"TMDB_API_KEY": "", "ATRIUM_MEDIA_ROOT": ""}, Options{Exec: ff.run})
	flPlaceholderCheck(t, w.get("/media/art/movie/"+py.URLQuote("Dune (2021)")), "poster without a TMDB key")
	flPlaceholderCheck(t, w.get("/media/thumb/"+py.URLQuote("tv/Lanterns (2026)/Season 01/x.mkv")), "thumbnail without a root")
	if tmdb.hitCount() != 0 || ff.count() != 0 {
		t.Errorf("unconfigured backends were used: %d TMDB requests, %d runs", tmdb.hitCount(), ff.count())
	}
}

func TestFlTMDBFailureGivesPlaceholderWithoutMarker(t *testing.T) {
	tmdb := flServeTMDB(t)
	w := flNew(t, map[string]string{"TMDB_API_KEY": "wrong"}, Options{})
	flPlaceholderCheck(t, w.get("/media/art/movie/"+py.URLQuote("Dune (2021)")), "TMDB rejects the key")
	if tmdb.imageCount() != 0 {
		t.Errorf("image fetched after TMDB error")
	}
	if _, err := os.Stat(filepath.Join(w.cfg.MediaCacheDir(), "posters", "movie", "Dune (2021).none")); !os.IsNotExist(err) {
		t.Errorf("TMDB error left a marker: %v", err)
	}
}
