package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/apps/library"
	"github.com/jeiang/atrium/internal/db"
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
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("lib", "library_search", map[string]any{"query": query})), asstReply("done"))
	thread := w.start(map[string]string{"text": "find " + query})
	if state := asstSettleThread(t, d, thread); state != "done" {
		t.Fatalf("library search run %s", state)
	}
	return lastToolResult(t, d, thread)
}

func TestLibraryReadsComeFromTheAgent(t *testing.T) {
	t.Parallel()
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
	if len(found.Results) != 1 || found.Results[0].Kind != library.KindMovie || len(found.Results[0].Files) != 1 || found.Results[0].Files[0] != "movies/"+libraryMovie+"/"+libraryMovie+".mkv" {
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
	for _, target := range []string{movie, "/apps/media/library/tv/" + libraryShow} {
		if code, body, _ := w.get(target, false); code != http.StatusOK || !strings.Contains(body, "Showing the library as of") {
			t.Fatalf("detail page %s from the stale catalogue = %d without the stale notice: %.300s", target, code, body)
		}
	}

	if out := unescape(toolResult(t, "library_search", map[string]any{"query": "dune"})); !strings.Contains(out, `"code":"unavailable"`) {
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
	t.Parallel()
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
