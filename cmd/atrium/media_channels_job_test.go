package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/library"
	"github.com/jeiang/atrium/internal/cli"
)

func TestMediaChannelsJobSkipsUntilAShowIsFollowed(t *testing.T) {
	var fetched atomic.Int64
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetched.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"torrents": []map[string]any{}})
	}))
	defer feed.Close()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, feed.URL+"/api/get-torrents", "", ""))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	tickChannels := func() {
		t.Helper()
		asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE name = ?", float64(time.Now().Unix())-1, library.JobChannels)
		if ran := runApp(t, "agent", "tick"); ran.code != cli.ExitOK {
			t.Fatalf("agent tick: exit %d %q", ran.code, ran.stderr)
		}
	}

	for range 2 {
		tickChannels()
	}
	if status, text := schedulerStatus(t, d, library.JobChannels); status != "skipped_unconfigured" || !strings.Contains(text, "no show is followed") {
		t.Fatalf("channels job %s %q with nothing followed, want skipped_unconfigured", status, text)
	}
	if n := schedulerRuns(t, d, library.JobChannels); n != 1 {
		t.Fatalf("%d channels job rows after two skipped runs, want one merged row", n)
	}
	if n := fetched.Load(); n != 0 || qb.count("add") != 0 {
		t.Fatalf("nothing followed: %d EZTV requests, %d adds, want none", n, qb.count("add"))
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM operation_audit WHERE operation = 'media.poll'"); n != 0 {
		t.Fatalf("%d media.poll audit rows for skipped runs, want 0", n)
	}

	base, _, stop := startServeStoppable(t, dir)
	b := mnpNewBrowser(t, base)
	b.want(http.MethodGet, "/apps/media/channels", nil, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/channels", url.Values{"name": {"Lanterns"}, "imdb": {"tt1234567"}, "pattern": {"1080p"}}, http.StatusOK)
	stop()
	if n := fetched.Load(); n != 0 {
		t.Fatalf("following a show without backfill made %d EZTV requests", n)
	}

	tickChannels()
	if status, text := schedulerStatus(t, d, library.JobChannels); status != "ok" {
		t.Fatalf("channels job %s %q with a followed show, want ok", status, text)
	}
	if n := fetched.Load(); n != 1 {
		t.Fatalf("%d EZTV requests after following one show, want 1", n)
	}
	mediaAudit(t, d, "media.poll", "noop")
}
