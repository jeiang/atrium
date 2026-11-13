package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
)

const (
	libraryMovie   = "Dune (2021)"
	libraryShow    = "Lanterns"
	libraryEpisode = "tv/Lanterns/Season 01/Lanterns - S01E01.mkv"
)

func libraryAgent(t *testing.T, dir string, env ...string) *mediaAgent {
	t.Helper()
	agent := startMediaAgent(t, mediaAgentOpts{main: true, dir: t.TempDir(), env: env})
	writeMediaFile(t, filepath.Join(agent.roots["movies"], libraryMovie, libraryMovie+".mkv"), "movie bytes")
	writeMediaFile(t, filepath.Join(agent.roots["movies"], libraryMovie, "notes.txt"), "not a video")
	writeMediaFile(t, filepath.Join(agent.roots["tv"], strings.TrimPrefix(libraryEpisode, "tv/")), "episode bytes")
	if err := os.MkdirAll(filepath.Join(agent.roots["tv"], libraryShow, "Season 02"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(defaultHostConfig)
	if err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "host-library.toml", withAgentConnection(string(raw), agent.base, filepath.Join(agent.dir, "token"))))
	return agent
}

type librarySearch struct {
	Results []struct {
		Kind    string   `json:"kind"`
		Title   string   `json:"title"`
		Files   []string `json:"files"`
		VPNURLs []string `json:"vpn_urls"`
	} `json:"results"`
	Catalogue string `json:"catalogue"`
	Code      string `json:"code"`
}

func lastToolResult(t *testing.T, d *db.DB, thread int64) librarySearch {
	t.Helper()
	var raw string
	if err := d.Read.QueryRow("SELECT content_json FROM thread_messages WHERE thread_id = ? AND role = 'user' AND content_json LIKE '%tool_result%' ORDER BY seq DESC LIMIT 1", thread).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var blocks []struct {
		Content any `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil || len(blocks) == 0 {
		t.Fatalf("tool result %s: %v", raw, err)
	}
	text, ok := blocks[0].Content.(string)
	if !ok {
		parts, _ := blocks[0].Content.([]any)
		for _, p := range parts {
			if m, _ := p.(map[string]any); m != nil {
				s, _ := m["text"].(string)
				text += s
			}
		}
	}
	var out librarySearch
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("tool result text %q: %v", text, err)
	}
	return out
}

func searchThroughServe(t *testing.T, api *modelLog, d *db.DB, w *asstWeb, query string) librarySearch {
	t.Helper()
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("lib", "media_library_search", map[string]any{"query": query})), asstReply("done"))
	thread := w.start(map[string]string{"text": "find " + query})
	if state := asstSettleThread(t, d, thread); state != "done" {
		t.Fatalf("library search run %s", state)
	}
	return lastToolResult(t, d, thread)
}

func TestLibraryReadsComeFromTheAgent(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	agent := libraryAgent(t, dir)
	asstWarm(t)
	d := asstDB(t, dir)
	w := asstBrowse(t, startServeIn(t, dir))

	movie := "/apps/media/library/movie/" + url.PathEscape(libraryMovie)
	for _, target := range []string{"/apps/media/library", movie, "/apps/media/library/tv/" + libraryShow} {
		if code, body, _ := w.get(target, false); code != http.StatusOK {
			t.Fatalf("GET %s = %d: %.300s", target, code, body)
		}
	}
	if code, _, _ := w.get("/apps/media/library/movie/Missing", false); code != http.StatusNotFound {
		t.Fatalf("missing movie = %d", code)
	}
	found := searchThroughServe(t, api, d, w, "lanterns")
	if found.Catalogue != "current" || len(found.Results) != 1 || found.Results[0].Title != libraryShow || len(found.Results[0].Files) != 1 || found.Results[0].Files[0] != libraryEpisode {
		t.Fatalf("library search %+v", found)
	}
	found = searchThroughServe(t, api, d, w, "dune")
	if len(found.Results) != 1 || found.Results[0].Kind != media.KindMovie || len(found.Results[0].Files) != 1 || found.Results[0].Files[0] != "movies/"+libraryMovie+"/"+libraryMovie+".mkv" {
		t.Fatalf("movie search %+v", found)
	}

	if err := os.RemoveAll(agent.roots["movies"]); err != nil {
		t.Fatal(err)
	}
	var stale librarySearch
	waitUntil(t, 60*time.Second, "the serve catalogue to turn stale", func() bool {
		if code, body, _ := w.get("/apps/media/library", false); code != http.StatusOK {
			t.Fatalf("library while the agent fails = %d: %.300s", code, body)
		}
		stale = searchThroughServe(t, api, d, w, "dune")
		if stale.Catalogue != "stale" {
			time.Sleep(time.Second)
			return false
		}
		return true
	})
	if len(stale.Results) != 1 || stale.Results[0].Title != "Dune" {
		t.Fatalf("a failed scan dropped catalogue entries: %+v", stale)
	}
	if code, body, _ := w.get(movie, false); code != http.StatusOK {
		t.Fatalf("movie page from the stale catalogue = %d: %.300s", code, body)
	}

	if out := unescape(toolResult(t, "media_library_search", map[string]any{"query": "dune"})); !strings.Contains(out, `"code":"unavailable"`) {
		t.Fatalf("a process without a catalogue = %s, want unavailable", out)
	}

	agent.terminate(t)
	for _, target := range []string{"/apps/media/library", movie} {
		if code, body, _ := w.get(target, false); code != http.StatusOK {
			t.Fatalf("GET %s with the agent stopped = %d: %.300s", target, code, body)
		}
	}
}

func TestLibraryWithoutAnyCatalogueIsUnavailable(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	agent := libraryAgent(t, dir)
	agent.terminate(t)
	w := asstBrowse(t, startServeIn(t, dir))
	if code, body, _ := w.get("/apps/media/library", false); code != http.StatusServiceUnavailable {
		t.Fatalf("library with an unreachable agent = %d, want 503: %.300s", code, body)
	}
	if code, body, _ := w.get("/apps/media/library/movie/"+url.PathEscape(libraryMovie), false); code != http.StatusServiceUnavailable {
		t.Fatalf("movie with an unreachable agent = %d, want 503: %.300s", code, body)
	}
}

func TestThumbnailRouteUsesTheAgent(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	bin := fakeFFmpeg(t, fakeThumbnailer)
	agent := libraryAgent(t, dir, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeMediaFile(t, filepath.Join(agent.roots["tv"], libraryShow, "Season 01", "Lanterns - S01E02.mkv"), "episode two")

	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE name = ?", float64(time.Now().Unix())-1, media.JobThumbnails)
	if got := runApp(t, "agent", "tick"); got.code != cli.ExitOK {
		t.Fatalf("agent tick: %d %s", got.code, got.stderr)
	}
	var details string
	if err := d.Read.QueryRow("SELECT details_json FROM operation_audit WHERE operation = 'media.thumbnails' ORDER BY id DESC LIMIT 1").Scan(&details); err != nil {
		t.Fatal(err)
	}
	var pass struct{ Generated, Remaining int }
	if err := json.Unmarshal([]byte(details), &pass); err != nil || pass.Generated != 2 || pass.Remaining != 0 || runCount(t, bin) != 2 {
		t.Fatalf("thumbnail pass %s runs=%d: %v", details, runCount(t, bin), err)
	}

	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	logs := &lockedBuffer{}
	base, _, stop, err := startServeAppArgs(t, defaultApp(t), logs, "--auth", "proxy", "--allowed-host", "127.0.0.1")
	if err != nil {
		t.Fatalf("%v: %s", err, logs.String())
	}
	t.Cleanup(stop)
	get := func(target string, groups string) (int, string, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if groups != "" {
			req.Header.Set("Remote-Groups", groups)
		}
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
	}
	const group = "atrium_access"
	thumb := "/apps/media/thumb/" + strings.ReplaceAll(url.PathEscape(libraryEpisode), "%2F", "/")
	if code, kind, body := get(thumb, group); code != http.StatusOK || kind != "image/jpeg" || body != fakeJPEG || runCount(t, bin) != 2 {
		t.Fatalf("thumbnail = %d %q %q runs=%d", code, kind, body, runCount(t, bin))
	}
	if code, kind, body := get("/apps/media/thumb/tv/"+libraryShow+"/missing.mkv", group); code != http.StatusOK || kind != "image/svg+xml" {
		t.Fatalf("missing thumbnail = %d %q %.100s", code, kind, body)
	}
	if code, kind, _ := get("/apps/media/thumb/downloads/x.mkv", group); code != http.StatusOK || kind != "image/svg+xml" {
		t.Fatalf("thumbnail outside the library = %d %q", code, kind)
	}
	if code, kind, _ := get(thumb+"?exp=4000000000&sig=AAAA", ""); code == http.StatusOK || kind == "image/jpeg" {
		t.Fatalf("a signed query opened the thumbnail without login: %d %q", code, kind)
	}

	agent.terminate(t)
	if code, kind, _ := get(thumb, group); code != http.StatusOK || kind != "image/svg+xml" {
		t.Fatalf("thumbnail with the agent stopped = %d %q", code, kind)
	}
}
