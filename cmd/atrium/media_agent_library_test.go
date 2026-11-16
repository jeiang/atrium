package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

func libraryPages(t *testing.T, a *mediaAgent) []protocol.Node {
	t.Helper()
	var nodes []protocol.Node
	cursor, snapshot := "", ""
	for pages := 0; ; pages++ {
		target := "/v1/library"
		if cursor != "" {
			target += "?cursor=" + url.QueryEscape(cursor)
		}
		resp, body := a.get(t, target, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("library page %d: %d %s", pages, resp.StatusCode, body)
		}
		var page protocol.LibraryPage
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		if snapshot != "" && page.SnapshotID != snapshot || len(page.Nodes) > protocol.LibraryPageSize || page.RootRevision == "" || page.ScannedAt.IsZero() {
			t.Fatalf("library page %d is inconsistent: %+v", pages, page)
		}
		snapshot = page.SnapshotID
		nodes = append(nodes, page.Nodes...)
		if page.Next == "" {
			return nodes
		}
		cursor = page.Next
	}
}

func TestMediaAgentScansTheLibraryInPages(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	want := map[string]string{}
	file := func(root, rel, content string) {
		writeMediaFile(t, filepath.Join(a.roots[root], rel), content)
		want[root+"/"+rel] = "file:" + strconv.Itoa(len(content))
	}
	dir := func(root, rel string) {
		if err := os.MkdirAll(filepath.Join(a.roots[root], rel), 0o755); err != nil {
			t.Fatal(err)
		}
		want[root+"/"+rel] = "dir"
	}
	dir("movies", "Alpha (2001)")
	file("movies", "Alpha (2001)/Alpha.mkv", "alpha")
	dir("movies", "Beta (2002)")
	file("movies", "Beta (2002)/Beta.mp4", "beta-movie")
	dir("tv", "Show")
	for _, season := range []string{"Season 01", "Season 02"} {
		dir("tv", "Show/"+season)
		file("tv", "Show/"+season+"/S01E01.mkv", season)
		file("tv", "Show/"+season+"/S01E01.en.srt", "subs")
	}
	dir("tv", "Show/Season 03")
	dir("tv", "Extras")
	for i := range 600 {
		file("tv", fmt.Sprintf("Extras/extra-%03d.nfo", i), strconv.Itoa(i))
	}
	if err := os.Symlink(filepath.Join(a.roots["movies"], "Alpha (2001)"), filepath.Join(a.roots["tv"], "link")); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, n := range libraryPages(t, a) {
		kind := "file:" + strconv.FormatInt(n.Size, 10)
		if n.Dir {
			kind = "dir"
		}
		key := n.Root + "/" + n.Rel
		if _, dup := got[key]; dup || n.Identity.Inode == 0 {
			t.Fatalf("node %s repeated or without identity", key)
		}
		got[key] = kind
	}
	if len(got) != len(want) {
		t.Fatalf("library has %d nodes, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("node %s = %q, want %q", k, got[k], v)
		}
	}
	resp, body := a.get(t, "/v1/library", nil)
	var page protocol.LibraryPage
	if resp.StatusCode != 200 || json.Unmarshal(body, &page) != nil || page.Next == "" {
		t.Fatalf("first page: %d %s", resp.StatusCode, body)
	}
	if resp, body := a.get(t, "/v1/library?cursor=0123:0", nil); resp.StatusCode != 410 || problemCode(t, body) != protocol.CodeSnapshotExpired {
		t.Fatalf("unknown cursor: %d %s", resp.StatusCode, body)
	}
	a.terminate(t)
	a = startMediaAgent(t, mediaAgentOpts{dir: a.dir})
	if resp, body := a.get(t, "/v1/library?cursor="+url.QueryEscape(page.Next), nil); resp.StatusCode != 410 || problemCode(t, body) != protocol.CodeSnapshotExpired {
		t.Fatalf("cursor after restart: %d %s", resp.StatusCode, body)
	}
	if err := os.Rename(a.roots["movies"], a.roots["movies"]+".gone"); err != nil {
		t.Fatal(err)
	}
	if resp, body := a.get(t, "/v1/library", nil); resp.StatusCode != 500 || problemCode(t, body) != protocol.CodeIOError {
		t.Fatalf("missing movies root: %d %s", resp.StatusCode, body)
	}
}

func TestMediaAgentScanSkipsUnreadableFolders(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads mode 000 directories")
	}
	a := startMediaAgent(t, mediaAgentOpts{})
	writeMediaFile(t, filepath.Join(a.roots["tv"], "Show", "ep.mkv"), "episode")
	hidden := filepath.Join(a.roots["tv"], "lost+found")
	writeMediaFile(t, filepath.Join(hidden, "inside.mkv"), "inside")
	if err := os.Chmod(hidden, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(hidden, 0o755) })
	var got []string
	for _, n := range libraryPages(t, a) {
		got = append(got, n.Root+"/"+n.Rel)
	}
	if want := []string{"tv/Show", "tv/Show/ep.mkv"}; !slices.Equal(got, want) {
		t.Fatalf("library = %q, want %q", got, want)
	}
	if err := os.Chmod(a.roots["movies"], 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a.roots["movies"], 0o755) })
	if resp, body := a.get(t, "/v1/library", nil); resp.StatusCode != 500 || problemCode(t, body) != protocol.CodeIOError {
		t.Fatalf("unreadable movies root: %d %s", resp.StatusCode, body)
	}
}
