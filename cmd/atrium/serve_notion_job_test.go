package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/passrun"
)

const notionJobPage = "11111111-1111-1111-1111-111111111111"

type heldNotion struct {
	*httptest.Server
	hits    atomic.Int64
	release chan struct{}
}

func newHeldNotion(t *testing.T, held bool) *heldNotion {
	t.Helper()
	n := &heldNotion{release: make(chan struct{})}
	if !held {
		close(n.release)
	}
	n.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.hits.Add(1)
		select {
		case <-n.release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer ntn_good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"object":"error","status":401,"code":"unauthorized","message":"API token is invalid."}`))
			return
		}
		switch r.URL.Path {
		case "/v1/search":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			if strings.Contains(string(body), `"page"`) {
				_, _ = w.Write([]byte(`{"object":"list","results":[{"object":"page","id":"` + notionJobPage + `","url":"https://notion.example/plan","last_edited_time":"2026-01-01T00:00:00.000Z","in_trash":false,"parent":{"type":"workspace"},"properties":{"title":{"type":"title","title":[{"plain_text":"Plan page"}]}}}],"has_more":false,"next_cursor":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false,"next_cursor":null}`))
		case "/v1/pages/" + notionJobPage + "/markdown":
			_, _ = w.Write([]byte(`{"markdown":"plan alpha beta","truncated":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(func() {
		select {
		case <-n.release:
		default:
			close(n.release)
		}
		n.Close()
	})
	return n
}

func TestServeSyncsNotionOnSchedule(t *testing.T) {
	fake := newHeldNotion(t, true)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", notionHostConfig(t, dir, fake.URL, "ntn_good"))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-3600, notion.JobKey)

	started := float64(time.Now().Unix())
	startServeIn(t, dir)
	waitUntil(t, 30*time.Second, "serve to prime the Notion job", func() bool {
		due := davNextDue(t, d, notion.JobKey)
		return due != nil && *due >= started
	})
	if due := davNextDue(t, d, notion.JobKey); *due < started+175 || *due > float64(time.Now().Unix())+185 {
		t.Fatalf("notion job next due %.0f s after serve start, want the 3 minute initial delay", *due-started)
	}
	if n := schedulerRuns(t, d, passrun.NotionSync); n != 0 || fake.hits.Load() != 0 {
		t.Fatalf("%d Notion job runs and %d Notion requests at serve start, want the missed run skipped", n, fake.hits.Load())
	}

	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-1, notion.JobKey)
	waitUntil(t, 60*time.Second, "the scheduled Notion run to reach Notion", func() bool { return fake.hits.Load() > 0 })
	manual := runApp(t, "notion", "sync")
	if manual.code == cli.ExitOK || !strings.Contains(manual.stderr, "held by another invocation") {
		t.Fatalf("notion sync beside the scheduled run = (%d, %q), want a held-lock refusal", manual.code, manual.stderr)
	}

	close(fake.release)
	waitUntil(t, 60*time.Second, "the Notion job run", schedulerTerminal(d, passrun.NotionSync))
	if status, text := schedulerStatus(t, d, passrun.NotionSync); status != "ok" {
		t.Fatalf("notion job status %s: %s", status, text)
	}
	if n := schedulerRuns(t, d, passrun.NotionSync); n != 1 {
		t.Fatalf("%d Notion job runs, want 1", n)
	}
	if ok, failed, _ := passRows(t, dir, passrun.NotionSync); ok != 1 || failed != 0 {
		t.Fatalf("notion-sync pass rows ok=%d failed=%d, want 1 and 0", ok, failed)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM notion_pages WHERE id = ?", notionJobPage); n != 1 {
		t.Fatalf("%d imported Notion pages, want 1", n)
	}
	if due := davNextDue(t, d, notion.JobKey); due == nil || *due < started+14*60 || *due > float64(time.Now().Unix())+15*60+1 {
		t.Fatalf("notion job next due %v after the run, want 15 minutes after its start", due)
	}
}

func TestNotionJobBacksOffAndSkipsWithoutAConnection(t *testing.T) {
	fake := newHeldNotion(t, false)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", notionHostConfig(t, dir, fake.URL, "ntn_bad"))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)

	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-1, notion.JobKey)
	before := float64(time.Now().Unix())
	ran := runApp(t, "agent", "tick")
	if status, text := schedulerStatus(t, d, passrun.NotionSync); status != "error" {
		t.Fatalf("notion job status %s (%s), tick exit %d %q, want an error from the bad token", status, text, ran.code, ran.stderr)
	}
	if due := davNextDue(t, d, notion.JobKey); due == nil || *due < before+59 || *due > float64(time.Now().Unix())+61 {
		t.Fatalf("notion job next due %v, want 1 minute after the failure", due)
	}
	if _, failed, _ := passRows(t, dir, passrun.NotionSync); failed != 1 {
		t.Fatalf("notion-sync failed pass rows %d, want 1", failed)
	}

	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFile(t, dir))
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-1, notion.JobKey)
	if ran := runApp(t, "agent", "tick"); ran.code != cli.ExitOK {
		t.Fatalf("agent tick without a Notion connection: exit %d %q", ran.code, ran.stderr)
	}
	if status, _ := schedulerStatus(t, d, passrun.NotionSync); status != "skipped_unconfigured" {
		t.Fatalf("notion job status %s without a connection, want skipped_unconfigured", status)
	}
	if ok, failed, _ := passRows(t, dir, passrun.NotionSync); ok != 0 || failed != 1 {
		t.Fatalf("notion-sync pass rows ok=%d failed=%d after the skip, want 0 and 1", ok, failed)
	}
}
