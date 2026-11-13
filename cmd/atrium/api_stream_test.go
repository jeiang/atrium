package main

import (
	"bufio"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

type streamSite struct {
	*apiSite
	api   *modelLog
	d     *db.DB
	f     *fixture
	token string
	stop  func()
}

func newStreamSite(t *testing.T) *streamSite {
	t.Helper()
	return newStreamSiteAt(t, nil)
}

func newStreamSiteAt(t *testing.T, now func() float64) *streamSite {
	t.Helper()
	dir, api, f := moduleFixtureAt(t, flagsExcept("", ""), now)
	warm(t, api)
	s := &streamSite{apiSite: &apiSite{t: t}, api: api, d: asstDB(t, dir), f: f, token: localAPIToken(t, "streams")}
	s.restart()
	t.Cleanup(func() { s.stop() })
	return s
}

func (s *streamSite) restart() {
	s.t.Helper()
	if s.stop != nil {
		s.stop()
	}
	base, _, stop, err := startServeAppArgs(s.t, s.f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		s.t.Fatal(err)
	}
	s.base, s.stop = base, stop
}

func (s *streamSite) start(text string) (int64, int64) {
	s.t.Helper()
	made := s.do(http.MethodPost, "/api/v1/threads", s.token, map[string]any{"text": text})
	if made.status != http.StatusCreated {
		s.t.Fatalf("create: %d %s", made.status, made.body)
	}
	thread := made.json["thread"].(map[string]any)
	return number(s.t, thread["id"]), number(s.t, made.json["run_id"])
}

func (s *streamSite) open(threadID, runID int64, headers ...string) *http.Response {
	s.t.Helper()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/threads/%d/runs/%d/events", s.base, threadID, runID), nil)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (s *streamSite) hold() func() {
	release := make(chan struct{})
	var once sync.Once
	s.api.SetOnRequest(func(anthropictest.Request) { <-release })
	free := func() { once.Do(func() { close(release) }) }
	s.t.Cleanup(free)
	return free
}

func waitRunning(t *testing.T, d *db.DB, runID int64) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for asstCount(t, d, "SELECT COUNT(*) FROM run_events WHERE stream_run_id = ?", runID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the run never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func frameIDs(stream string) []string {
	var ids []string
	for _, line := range strings.Split(stream, "\n") {
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func TestRunEventsPersistAcrossRuns(t *testing.T) {
	s := newStreamSite(t)
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("first stream")))
	threadID, runID := s.start("first")
	first, _ := s.events(s.token, threadID, runID, "")
	if n := strings.Count(first, "event: complete\n"); n != 1 {
		t.Fatalf("%d terminal events: %s", n, first)
	}
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("second stream")))
	sent := s.do(http.MethodPost, fmt.Sprintf("/api/v1/threads/%d/messages", threadID), s.token, map[string]any{"text": "second"})
	if sent.status != http.StatusAccepted {
		t.Fatalf("send: %d %s", sent.status, sent.body)
	}
	s.events(s.token, threadID, number(t, sent.json["run_id"]), "")
	if replay, _ := s.events(s.token, threadID, runID, ""); replay != first {
		t.Fatalf("a later run changed the first stream:\n%s\nwas:\n%s", replay, first)
	}
	if n := asstCount(t, s.d, "SELECT COUNT(*) FROM run_streams WHERE run_id = ? AND terminal_seq = next_seq - 1 AND terminal_at IS NOT NULL", runID); n != 1 {
		t.Fatalf("terminal metadata rows: %d", n)
	}
	if n := asstCount(t, s.d, "SELECT COUNT(*) FROM run_events WHERE stream_run_id = ?", runID); n != len(frameIDs(first)) {
		t.Fatalf("durable events: %d, frames %d", n, len(frameIDs(first)))
	}
	s.restart()
	if replay, _ := s.events(s.token, threadID, runID, ""); replay != first {
		t.Fatal("restart changed the durable stream")
	}
	browser := asstBrowse(t, s.base)
	if code, body, _ := browser.post(fmt.Sprintf("/chat/%d/delete", threadID), url.Values{}, true); code != http.StatusOK && code != http.StatusSeeOther && code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", code, body)
	}
	if n := asstCount(t, s.d, "SELECT COUNT(*) FROM run_events WHERE run_id = ? OR stream_run_id = ?", runID, runID); n != 0 {
		t.Fatalf("%d events retained after deletion", n)
	}
}

func TestStreamResumesWithoutGaps(t *testing.T) {
	s := newStreamSite(t)
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Thinking("pondering", "sig"), anthropictest.Text("resumed answer")))
	threadID, runID := s.start("resume")
	full, last := s.events(s.token, threadID, runID, "")
	ids := frameIDs(full)
	if len(ids) < 3 || ids[len(ids)-1] != last {
		t.Fatalf("frames %v last %s", ids, last)
	}
	for i, id := range ids[:len(ids)-1] {
		replay, _ := s.events(s.token, threadID, runID, id)
		if got := frameIDs(replay); strings.Join(got, ",") != strings.Join(ids[i+1:], ",") {
			t.Fatalf("resume after %s replayed %v, want %v", id, got, ids[i+1:])
		}
	}
	s.restart()
	replay, _ := s.events(s.token, threadID, runID, ids[0])
	if got := frameIDs(replay); strings.Join(got, ",") != strings.Join(ids[1:], ",") {
		t.Fatalf("resume after restart replayed %v", got)
	}
	if resp := s.open(threadID, runID, "Last-Event-ID", last); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("resume at the terminal: %d", resp.StatusCode)
	}
	path := fmt.Sprintf("/api/v1/threads/%d/runs/%d/events?after=0", threadID, runID)
	if got := s.do(http.MethodGet, path, s.token, nil, "Last-Event-ID", last); got.status != http.StatusNoContent {
		t.Fatalf("Last-Event-ID did not win over after: %d", got.status)
	}
	end, _ := strconv.Atoi(last)
	expectAPI(t, s.do(http.MethodGet, path, s.token, nil, "Last-Event-ID", strconv.Itoa(end+1)), http.StatusBadRequest, "bad_request")
	expectAPI(t, s.do(http.MethodGet, path, s.token, nil, "Last-Event-ID", "x"), http.StatusBadRequest, "bad_request")
	expectAPI(t, s.do(http.MethodGet, fmt.Sprintf("/api/v1/threads/%d/runs/%d/events", threadID, runID+100), s.token, nil), http.StatusNotFound, "not_found")
}

func TestStreamRevokedBearerStops(t *testing.T) {
	s := newStreamSite(t)
	free := s.hold()
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("late")))
	threadID, runID := s.start("hold")
	waitRunning(t, s.d, runID)
	resp := s.open(threadID, runID)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "private, no-store, no-transform" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream: %d %v", resp.StatusCode, resp.Header)
	}
	if got := runApp(t, "auth", "token", "revoke", strconv.Itoa(asstCount(t, s.d, "SELECT id FROM auth_bearers WHERE name = 'streams'"))); got.code != cli.ExitOK {
		t.Fatalf("revoke: %d %s", got.code, got.stderr)
	}
	closed := make(chan struct{})
	go func() {
		scan := bufio.NewScanner(resp.Body)
		for scan.Scan() {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(20 * time.Second):
		t.Fatal("the stream stayed open after the bearer was revoked")
	}
	free()
}

func TestStreamCaps(t *testing.T) {
	s := newStreamSite(t)
	free := s.hold()
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("late")))
	threadID, runID := s.start("caps")
	waitRunning(t, s.d, runID)
	var open []*http.Response
	for range 4 {
		resp := s.open(threadID, runID)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: %d", len(open)+1, resp.StatusCode)
		}
		open = append(open, resp)
	}
	path := fmt.Sprintf("/api/v1/threads/%d/runs/%d/events", threadID, runID)
	expectAPI(t, s.do(http.MethodGet, path, s.token, nil), http.StatusTooManyRequests, "rate_limited")
	_ = open[0].Body.Close()
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp := s.open(threadID, runID)
		if resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a closed stream kept its slot: %d", resp.StatusCode)
		}
		_ = resp.Body.Close()
		time.Sleep(100 * time.Millisecond)
	}
	free()
	stream := bufio.NewScanner(open[1].Body)
	for stream.Scan() {
		if stream.Text() == "event: complete" {
			return
		}
	}
	t.Fatal("an open stream ended without its terminal event")
}

func TestStreamExpiredIs410(t *testing.T) {
	var shift atomic.Int64
	s := newStreamSiteAt(t, func() float64 {
		return float64(time.Now().Add(time.Duration(shift.Load())).UnixMicro()) / 1e6
	})
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("old answer")))
	threadID, runID := s.start("old")
	_, last := s.events(s.token, threadID, runID, "")
	shift.Store(int64(31 * 24 * time.Hour))
	free := s.hold()
	for range 8 {
		s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("late")))
	}
	activeThread, activeRun := s.start("active")
	waitRunning(t, s.d, activeRun)
	kept := asstCount(t, s.d, "SELECT COUNT(*) FROM run_events WHERE stream_run_id = ?", activeRun)
	schedulerSeed(t, s.f, s.d)
	schedulerDue(t, s.d, "stream-retention")
	schedulerRun(t, s.f, s.d, func() bool {
		return asstCount(t, s.d, "SELECT COUNT(*) FROM run_streams WHERE run_id = ? AND retained_after = terminal_seq", runID) == 1
	})

	path := fmt.Sprintf("/api/v1/threads/%d/runs/%d/events", threadID, runID)
	expired := s.do(http.MethodGet, path, s.token, nil)
	expectAPI(t, expired, http.StatusGone, "stream_expired")
	if number(t, expired.json["run_id"]) != runID {
		t.Fatalf("expired answer names run %v", expired.json["run_id"])
	}
	if got := s.do(http.MethodGet, path, s.token, nil, "Last-Event-ID", last); got.status != http.StatusNoContent {
		t.Fatalf("resume at the pruned terminal: %d", got.status)
	}
	if n := asstCount(t, s.d, "SELECT COUNT(*) FROM run_events WHERE stream_run_id = ?", runID); n != 0 {
		t.Fatalf("%d expired events kept", n)
	}
	if n := asstCount(t, s.d, "SELECT COUNT(*) FROM run_events WHERE stream_run_id = ?", activeRun); n < kept {
		t.Fatalf("the active stream lost events: %d of %d", n, kept)
	}
	if detail := s.do(http.MethodGet, fmt.Sprintf("/api/v1/threads/%d", threadID), s.token, nil); detail.status != http.StatusOK {
		t.Fatalf("thread detail after pruning: %d", detail.status)
	}
	if resp := s.open(activeThread, activeRun); resp.StatusCode != http.StatusOK {
		t.Fatalf("active stream after pruning: %d", resp.StatusCode)
	}
	free()
}
