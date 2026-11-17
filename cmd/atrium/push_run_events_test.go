package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

type runPushSite struct {
	*streamSite
	push    *fakePush
	browser pushBrowser
}

func newRunPushSite(t *testing.T, flags map[string]string) *runPushSite {
	t.Helper()
	dir, api, f := moduleFixture(t, flags)
	push, browser := pushRig(t, dir)
	warm(t, api)
	s := &runPushSite{
		streamSite: &streamSite{apiSite: &apiSite{t: t}, api: api, d: asstDB(t, dir), f: f, token: localAPIToken(t, "run-push")},
		push:       push, browser: browser,
	}
	browser.store(t, s.d)
	s.restart()
	t.Cleanup(func() { s.stop() })
	return s
}

func (s *runPushSite) pushes(title string) []map[string]any {
	s.t.Helper()
	s.push.mu.Lock()
	received := append([]pushRequest{}, s.push.received...)
	s.push.mu.Unlock()
	var out []map[string]any
	for _, r := range received {
		if got := s.browser.decrypt(s.t, r.body); got["title"] == title {
			out = append(out, got)
		}
	}
	return out
}

func (s *runPushSite) waitPushes(title string, n int) []map[string]any {
	s.t.Helper()
	waitUntil(s.t, 30*time.Second, fmt.Sprintf("%d %q pushes", n, title), func() bool { return len(s.pushes(title)) >= n })
	return s.pushes(title)
}

func (s *runPushSite) waitRun(runID int64, state string) {
	s.t.Helper()
	waitUntil(s.t, 30*time.Second, fmt.Sprintf("run %d %s", runID, state), func() bool {
		return asstCount(s.t, s.d, "SELECT COUNT(*) FROM runs WHERE id = ? AND state = ?", runID, state) == 1
	})
}

func latestRun(t *testing.T, s *runPushSite, threadID int64) int64 {
	t.Helper()
	return int64(asstCount(t, s.d, "SELECT MAX(id) FROM runs WHERE thread_id = ?", threadID))
}

const (
	questionPush = "atrium: question waiting"
	failedPush   = "atrium: run failed"
	repairPush   = "atrium: Grafana failing"
)

func TestPushOnceForAParkedQuestionAFailedRunAndANewRepair(t *testing.T) {
	t.Parallel()
	s := newRunPushSite(t, flagsExcept("", ""))

	repairs := s.waitPushes(repairPush, 1)
	if repairs[0]["url"] != "/settings/connections" {
		t.Fatalf("repair push link: %v", repairs[0])
	}
	if got := runApp(t, "connections", "test", "service/grafana"); got.code != cli.ExitError {
		t.Fatalf("unchanged failing check: exit %d %q", got.code, got.stdout)
	}

	s.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("ask-1", "ask_operator", map[string]any{"question": "Which private calendar should I use?"})))
	threadID, runID := s.start("plan the week")
	s.waitRun(runID, "parked")
	questions := s.waitPushes(questionPush, 1)
	link := fmt.Sprintf("/chat/%d", threadID)
	if q := questions[0]; q["url"] != link || !strings.Contains(fmt.Sprint(q["body"]), "waiting for your answer") || strings.Contains(fmt.Sprint(q["body"]), "private calendar") {
		t.Fatalf("question push: %v", q)
	}
	s.api.Push(asstReply("noted"))
	answered := s.do(http.MethodPost, fmt.Sprintf("/api/v1/threads/%d/answer", threadID), s.token, map[string]any{"text": "personal"})
	if answered.status/100 != 2 {
		t.Fatalf("answer: %d %s", answered.status, answered.body)
	}
	s.waitRun(runID, "done")
	if unread := asstCount(t, s.d, "SELECT COUNT(*) FROM notifications WHERE title = ? AND read_at IS NULL", questionPush); unread != 0 {
		t.Fatalf("%d unread question notifications after the answer, want 0", unread)
	}

	s.api.Push(anthropictest.Failure(400, "invalid_request_error", "private model words"))
	sent := s.do(http.MethodPost, fmt.Sprintf("/api/v1/threads/%d/messages", threadID), s.token, map[string]any{"text": "and tomorrow?"})
	if sent.status/100 != 2 {
		t.Fatalf("message: %d %s", sent.status, sent.body)
	}
	s.waitRun(latestRun(t, s, threadID), "failed")
	failures := s.waitPushes(failedPush, 1)
	if f := failures[0]; f["url"] != link || !strings.Contains(fmt.Sprint(f["body"]), "the model service returned an error") || strings.Contains(fmt.Sprint(f["body"]), "private model words") {
		t.Fatalf("failure push: %v", f)
	}

	free := s.hold()
	s.api.Push(anthropictest.Failure(400, "invalid_request_error", "watched failure"))
	watchedThread, watchedRun := s.start("watched")
	waitRunning(t, s.d, watchedRun)
	lines := streamLines(s.open(watchedThread, watchedRun).Body)
	awaitLine(t, lines, 5*time.Second, "event: run_start")
	free()
	awaitDone(t, lines, 10*time.Second, "failed")

	s.api.Push(anthropictest.Failure(400, "invalid_request_error", "unwatched failure"))
	lastThread, lastRun := s.start("unwatched")
	s.waitRun(lastRun, "failed")
	failures = s.waitPushes(failedPush, 2)
	var links []string
	for _, f := range failures {
		links = append(links, fmt.Sprint(f["url"]))
	}
	if want := []string{link, fmt.Sprintf("/chat/%d", lastThread)}; strings.Join(links, " ") != strings.Join(want, " ") {
		t.Fatalf("failure push links %v, want %v (none for the watched run)", links, want)
	}

	if n := len(s.pushes(questionPush)); n != 1 {
		t.Fatalf("%d question pushes, want 1", n)
	}
	if n := len(s.pushes(repairPush)); n != 1 {
		t.Fatalf("%d repair pushes after an unchanged failing check, want 1", n)
	}
}

func TestPushOnAskAndFailureSendsNothingWithNotificationsOff(t *testing.T) {
	t.Parallel()
	s := newRunPushSite(t, flagsExcept("notifications", "false"))
	s.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("ask-1", "ask_operator", map[string]any{"question": "Which calendar?"})))
	_, asked := s.start("plan")
	s.waitRun(asked, "parked")
	s.api.Push(anthropictest.Failure(400, "invalid_request_error", "refused"))
	_, failed := s.start("fail")
	s.waitRun(failed, "failed")
	if got := runApp(t, "connections", "test", "service/grafana"); got.code != cli.ExitError {
		t.Fatalf("failing check: exit %d %q", got.code, got.stdout)
	}
	if n := s.push.count(); n != 0 {
		t.Fatalf("%d pushes with notifications off, want 0", n)
	}
	if n := asstCount(t, s.d, "SELECT COUNT(*) FROM notifications"); n != 0 {
		t.Fatalf("%d notifications with notifications off, want 0", n)
	}
}
