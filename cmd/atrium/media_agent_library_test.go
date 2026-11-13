package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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

func fakeFFmpeg(t *testing.T, ffmpeg string) string {
	t.Helper()
	bin := t.TempDir()
	scripts := map[string]string{
		"ffprobe": "#!/bin/sh\necho 100\n",
		"ffmpeg":  "#!/bin/sh\necho run >> " + filepath.Join(bin, "runs") + "\n" + ffmpeg,
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

func runCount(t *testing.T, bin string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(bin, "runs"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "run\n")
}

const (
	fakeJPEG        = "\xff\xd8\xff\xe0fake-jpeg"
	fakeThumbnailer = "if [ \"$(head -c 4 /dev/fd/3)\" = fail ]; then exit 1; fi\nprintf '\\377\\330\\377\\340fake-jpeg'\n"
)

func TestMediaAgentThumbnails(t *testing.T) {
	t.Parallel()
	bin := fakeFFmpeg(t, fakeThumbnailer)
	a := startMediaAgent(t, mediaAgentOpts{env: []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}})
	resp, body := a.get(t, "/health", nil)
	var health protocol.Health
	if err := json.Unmarshal(body, &health); err != nil || !slices.Contains(health.Capabilities, protocol.CapabilityThumbnail) || !slices.Contains(health.Capabilities, protocol.CapabilityScan) {
		t.Fatalf("capabilities %d %s", resp.StatusCode, body)
	}
	episode := filepath.Join(a.roots["tv"], "Show", "ep1.mkv")
	writeMediaFile(t, episode, "episode one")
	thumbs := func() int {
		matches, err := filepath.Glob(filepath.Join(a.dir, "cache", "thumbs", "*.jpg"))
		if err != nil {
			t.Fatal(err)
		}
		return len(matches)
	}
	get := func(rel string) (*http.Response, []byte) {
		return a.get(t, "/v1/thumbnail?root=tv&path="+url.QueryEscape(rel), nil)
	}
	resp, body = get("Show/ep1.mkv")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || string(body) != fakeJPEG {
		t.Fatalf("thumbnail: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if resp, body = get("Show/ep1.mkv"); resp.StatusCode != 200 || string(body) != fakeJPEG || runCount(t, bin) != 1 || thumbs() != 1 {
		t.Fatalf("cached thumbnail regenerated: runs=%d thumbs=%d", runCount(t, bin), thumbs())
	}
	if resp, _ := a.do(t, http.MethodHead, "/v1/thumbnail?root=tv&path=Show/ep1.mkv", nil); resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumbnail HEAD: %d", resp.StatusCode)
	}
	replacement := episode + ".new"
	writeMediaFile(t, replacement, "episode one, remastered")
	if err := os.Rename(replacement, episode); err != nil {
		t.Fatal(err)
	}
	if resp, _ = get("Show/ep1.mkv"); resp.StatusCode != 200 || runCount(t, bin) != 2 || thumbs() != 2 {
		t.Fatalf("replaced video: %d runs=%d thumbs=%d", resp.StatusCode, runCount(t, bin), thumbs())
	}
	writeMediaFile(t, filepath.Join(a.roots["tv"], "Show", "broken.mkv"), "fail")
	for range 2 {
		if resp, body = get("Show/broken.mkv"); resp.StatusCode != 404 || problemCode(t, body) != protocol.CodeNotFound {
			t.Fatalf("broken video: %d %s", resp.StatusCode, body)
		}
	}
	if runCount(t, bin) != 3 {
		t.Fatalf("a remembered miss ran ffmpeg again: %d", runCount(t, bin))
	}
	if resp, body = get("Show/missing.mkv"); resp.StatusCode != 404 {
		t.Fatalf("missing video: %d %s", resp.StatusCode, body)
	}
	var paths []protocol.Path
	for i := range 3 {
		rel := fmt.Sprintf("Show/pass-%d.mkv", i)
		writeMediaFile(t, filepath.Join(a.roots["tv"], rel), rel)
		paths = append(paths, protocol.Path{Root: "tv", Rel: rel})
	}
	status, body := a.json(t, http.MethodPost, "/v1/thumbnails/pass", protocol.ThumbnailPass{Paths: paths, Limit: 2})
	var result protocol.ThumbnailResult
	if status != 200 || json.Unmarshal(body, &result) != nil || result != (protocol.ThumbnailResult{Generated: 2, Remaining: 1}) {
		t.Fatalf("pass: %d %s", status, body)
	}
	status, body = a.json(t, http.MethodPost, "/v1/thumbnails/pass", protocol.ThumbnailPass{Paths: []protocol.Path{{Root: "import", Rel: "item/ep.mkv"}}, Limit: 2})
	if status != 400 || problemCode(t, body) != protocol.CodeUnsafePath {
		t.Fatalf("pass outside the library: %d %s", status, body)
	}
	if resp, body = a.get(t, "/v1/thumbnail?root=import&path=item/ep.mkv", nil); resp.StatusCode != 400 {
		t.Fatalf("thumbnail outside the library: %d %s", resp.StatusCode, body)
	}
	status, body = a.json(t, http.MethodPost, "/v1/thumbnails/pass", protocol.ThumbnailPass{Paths: make([]protocol.Path, 1001), Limit: 2})
	if status != 413 || problemCode(t, body) != protocol.CodeRequestTooLarge {
		t.Fatalf("oversized pass: %d %s", status, body)
	}
	empty := t.TempDir()
	plain := startMediaAgent(t, mediaAgentOpts{env: []string{"PATH=" + empty}})
	_, body = plain.get(t, "/health", nil)
	if err := json.Unmarshal(body, &health); err != nil || slices.Contains(health.Capabilities, protocol.CapabilityThumbnail) {
		t.Fatalf("thumbnail advertised without ffmpeg: %s", body)
	}
	writeMediaFile(t, filepath.Join(plain.roots["tv"], "ep.mkv"), "episode")
	if resp, body = plain.get(t, "/v1/thumbnail?root=tv&path=ep.mkv", nil); resp.StatusCode != 503 || problemCode(t, body) != protocol.CodeUnavailable {
		t.Fatalf("thumbnail without ffmpeg: %d %s", resp.StatusCode, body)
	}
}

func TestMediaAgentThumbnailCancelKillsTheFFmpegTree(t *testing.T) {
	t.Parallel()
	pids := filepath.Join(t.TempDir(), "pids")
	bin := fakeFFmpeg(t, "echo $$ >> "+pids+"\nsleep 600 &\necho $! >> "+pids+"\nwait\n")
	a := startMediaAgent(t, mediaAgentOpts{env: []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}})
	writeMediaFile(t, filepath.Join(a.roots["movies"], "Film", "film.mkv"), "film")
	finished := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, a.base+"/v1/thumbnail?root=movies&path=Film/film.mkv", nil)
		req.Host = mediaAgentHost
		req.Header.Set("Authorization", "Bearer "+a.token)
		resp, err := mediaAgentClient.Do(req)
		if err != nil {
			finished <- 0
			return
		}
		_ = resp.Body.Close()
		finished <- resp.StatusCode
	}()
	var ids []int
	waitFor(t, func() bool {
		b, _ := os.ReadFile(pids)
		ids = ids[:0]
		for _, f := range strings.Fields(string(b)) {
			n, err := strconv.Atoi(f)
			if err == nil {
				ids = append(ids, n)
			}
		}
		return len(ids) == 2
	})
	stopped := a.terminate(t)
	if stopped > 10*time.Second {
		t.Fatalf("drain waited for ffmpeg: %s", stopped)
	}
	if status := <-finished; status != 503 && status != 0 {
		t.Fatalf("cancelled thumbnail answered %d", status)
	}
	for _, pid := range ids {
		waitFor(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH })
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

func TestMediaAgentThumbnailAgentFailuresAreNotRemembered(t *testing.T) {
	t.Parallel()
	bin := fakeFFmpeg(t, "if [ -e "+"\"$0.die\" ]; then kill -9 $$; fi\n"+fakeThumbnailer)
	a := startMediaAgent(t, mediaAgentOpts{env: []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}})
	writeMediaFile(t, filepath.Join(a.roots["tv"], "ep.mkv"), "episode")
	die := filepath.Join(bin, "ffmpeg.die")
	writeMediaFile(t, die, "")
	thumbs := filepath.Join(a.dir, "cache", "thumbs")
	markers := func() int {
		matches, err := filepath.Glob(filepath.Join(thumbs, "*.none"))
		if err != nil {
			t.Fatal(err)
		}
		return len(matches)
	}
	get := func() (*http.Response, []byte) { return a.get(t, "/v1/thumbnail?root=tv&path=ep.mkv", nil) }
	if resp, body := get(); resp.StatusCode != 503 || problemCode(t, body) != protocol.CodeUnavailable || markers() != 0 || runCount(t, bin) != 1 {
		t.Fatalf("killed ffmpeg: %d %s markers=%d runs=%d", resp.StatusCode, body, markers(), runCount(t, bin))
	}
	if err := os.Remove(die); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(thumbs, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(thumbs, 0o700) })
	if os.Geteuid() != 0 {
		if resp, body := get(); resp.StatusCode != 503 || problemCode(t, body) != protocol.CodeUnavailable || markers() != 0 || runCount(t, bin) != 2 {
			t.Fatalf("unwritable cache: %d %s markers=%d runs=%d", resp.StatusCode, body, markers(), runCount(t, bin))
		}
	}
	if err := os.Chmod(thumbs, 0o700); err != nil {
		t.Fatal(err)
	}
	if resp, body := get(); resp.StatusCode != 200 || string(body) != fakeJPEG {
		t.Fatalf("thumbnail after the agent recovered: %d %s", resp.StatusCode, body)
	}
}

func TestMediaAgentThumbnailCacheIsPrunedAtStartup(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	a.terminate(t)
	thumbs := filepath.Join(a.dir, "cache", "thumbs")
	old := time.Now().Add(-8 * 24 * time.Hour)
	files := map[string]bool{".thumb-123.jpg": false, "aa.jpg.none": false, "bb.jpg.none": true, "cc.jpg": true}
	for name := range files {
		writeMediaFile(t, filepath.Join(thumbs, name), "x")
		if name != "bb.jpg.none" {
			if err := os.Chtimes(filepath.Join(thumbs, name), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	startMediaAgent(t, mediaAgentOpts{dir: a.dir})
	for name, kept := range files {
		if _, err := os.Lstat(filepath.Join(thumbs, name)); (err == nil) != kept {
			t.Fatalf("%s kept=%v after startup: %v", name, kept, err)
		}
	}
}

func TestMediaAgentThumbnailsWithRealFFmpeg(t *testing.T) {
	t.Parallel()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	a := startMediaAgent(t, mediaAgentOpts{})
	show := filepath.Join(a.roots["tv"], "Show")
	if err := os.MkdirAll(show, 0o755); err != nil {
		t.Fatal(err)
	}
	videos := []string{"ep.mkv", "ep.mp4", "ep.avi", "ep.ts", "secret.mkv"}
	for _, name := range videos {
		out, err := exec.Command(ffmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc=d=2:s=320x240", "-frames:v", "20", filepath.Join(show, name)).CombinedOutput()
		if err != nil {
			t.Fatalf("make %s: %v %s", name, err, out)
		}
	}
	writeMediaFile(t, filepath.Join(show, "concat.mkv"), "ffconcat version 1.0\nfile secret.mkv\nfile "+filepath.Join(show, "secret.mkv")+"\n")
	writeMediaFile(t, filepath.Join(show, "playlist.mkv"), "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\nsecret.mkv\n#EXT-X-ENDLIST\n")
	get := func(rel string) (*http.Response, []byte) {
		return a.get(t, "/v1/thumbnail?root=tv&path="+url.QueryEscape("Show/"+rel), nil)
	}
	for _, name := range videos[:4] {
		if resp, body := get(name); resp.StatusCode != 200 || !strings.HasPrefix(string(body), "\xff\xd8\xff") {
			t.Fatalf("thumbnail of %s: %d %.200q", name, resp.StatusCode, body)
		}
	}
	jpegs, err := filepath.Glob(filepath.Join(a.dir, "cache", "thumbs", "*.jpg"))
	if err != nil || len(jpegs) != 4 {
		t.Fatalf("cached thumbnails = %d %v", len(jpegs), err)
	}
	for _, name := range []string{"concat.mkv", "playlist.mkv"} {
		resp, body := get(name)
		if resp.StatusCode != 404 || problemCode(t, body) != protocol.CodeNotFound || strings.Contains(string(body), "\xff\xd8") {
			t.Fatalf("playlist-style %s answered %d %.200q", name, resp.StatusCode, body)
		}
	}
	if after, err := filepath.Glob(filepath.Join(a.dir, "cache", "thumbs", "*.jpg")); err != nil || len(after) != 4 {
		t.Fatalf("a playlist-style file produced a thumbnail: %d %v", len(after), err)
	}
}
