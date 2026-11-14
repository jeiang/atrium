//go:build !modernc

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
)

func agentConnectionState(t *testing.T, dir string) string {
	t.Helper()
	var state string
	if err := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t)).Read.QueryRow(
		"SELECT state FROM connections WHERE id = 'service/media-agent'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestCancelledLibraryRequestsLeaveTheAgentConnectionOK(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	bin := fakeFFmpeg(t, fakeThumbnailer)
	agent := libraryAgent(t, dir, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for i := 2; i <= 6; i++ {
		writeMediaFile(t, filepath.Join(agent.roots["tv"], libraryShow, "Season 01", fmt.Sprintf("Lanterns - S01E%02d.mkv", i)), "episode")
	}
	for i := 1; i <= 6; i++ {
		path := filepath.Join(libraryShow, "Season 01", fmt.Sprintf("Lanterns - S01E%02d.mkv", i))
		resp, image := agent.get(t, "/v1/thumbnail?"+url.Values{"root": {"tv"}, "path": {path}}.Encode(), nil)
		if resp.StatusCode != http.StatusOK || string(image) != fakeJPEG {
			t.Fatalf("prepare thumbnail = %d %q", resp.StatusCode, image)
		}
	}
	if ran := runApp(t, "connections", "test", "service/media-agent", "--json"); ran.code != cli.ExitOK || !strings.Contains(ran.stdout, `"state": "ok"`) {
		t.Fatalf("connection test = %d %s %s", ran.code, ran.stdout, ran.stderr)
	}
	pause := func() {
		if err := agent.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
	}
	resume := func() { _ = agent.cmd.Process.Signal(syscall.SIGCONT) }
	t.Cleanup(resume)

	pause()
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	logs := &lockedBuffer{}
	base, _, stop, err := startServeAppArgs(t, defaultApp(t), logs, "--auth", "proxy", "--allowed-host", "127.0.0.1")
	if err != nil {
		resume()
		t.Fatalf("%v: %s", err, logs.String())
	}
	t.Cleanup(stop)
	request := func(ctx context.Context, target string) (int, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Remote-Groups", "atrium_access")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), err
	}
	cancelled := func(target string) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if code, _, err := request(ctx, target); err == nil {
			t.Errorf("%s answered %d while the agent was paused", target, code)
		}
	}
	cancelled("/apps/media/library")
	resume()

	var index string
	for deadline := time.Now().Add(15 * time.Second); ; {
		code, body, err := request(context.Background(), "/apps/media/library")
		if err == nil && code == http.StatusOK && strings.Contains(body, libraryShow) {
			index = body
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the library after a cancelled first load = %d %v %.300s", code, err, body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if strings.Contains(index, "Showing the library as of") {
		t.Fatalf("a cancelled first load marked the catalogue stale: %s", index)
	}
	code, show, err := request(context.Background(), "/apps/media/library/tv/"+libraryShow)
	if err != nil || code != http.StatusOK {
		t.Fatalf("show page = %d %v", code, err)
	}
	thumbs := regexp.MustCompile(`src="(/apps/media/thumb/[^"]+)"`).FindAllStringSubmatch(show, -1)
	if len(thumbs) < 6 {
		t.Fatalf("show page thumbnails = %d", len(thumbs))
	}
	pause()
	var wg sync.WaitGroup
	for _, m := range thumbs {
		wg.Go(func() { cancelled(m[1]) })
	}
	wg.Wait()
	resume()
	code, image, err := request(context.Background(), thumbs[0][1])
	if err != nil || code != http.StatusOK || image != fakeJPEG {
		t.Fatalf("thumbnail after the cancelled burst = %d %v %q", code, err, image)
	}
	if state := agentConnectionState(t, dir); state != "ok" {
		t.Fatalf("cancelled requests left the agent connection %q", state)
	}
	if n := connectionNotices(t, dir, "Media agent"); n != 0 {
		t.Fatalf("cancelled requests recorded %d connection notifications", n)
	}
	if strings.Contains(logs.String(), "media catalogue refresh failed") {
		t.Fatalf("a cancelled load failed the catalogue refresh: %s", logs.String())
	}
}
