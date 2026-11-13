package main

import (
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/web"
)

func threadPath(id int64, rest string) string {
	return "/api/v1/threads/" + strconv.FormatInt(id, 10) + rest
}

func waitThreadSettled(t *testing.T, s *apiSite, token string, id int64) apiReply {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		view := s.do(http.MethodGet, threadPath(id, ""), token, nil)
		queued, _ := view.json["queued"].([]any)
		if view.status == http.StatusOK && view.json["active_run"] == nil && len(queued) == 0 {
			return view
		}
		if time.Now().After(deadline) {
			t.Fatalf("thread %d never settled: %s", id, view.body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func transcriptItems(view apiReply) []map[string]any {
	raw, _ := view.json["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, _ := item.(map[string]any)
		out = append(out, m)
	}
	return out
}

func TestThreadsAPIActions(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	d := asstDB(t, dir)
	s := startAPISite(t, dir, api)
	owner := s.token("owner")

	free := asstHold(t, api)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("first answer")), anthropictest.Reply("end_turn", anthropictest.Text("queued answer")))
	requests := len(api.Requests())

	started := s.do(http.MethodPost, "/api/v1/threads", owner, map[string]any{"text": "hello", "web_search": false})
	if started.status != http.StatusCreated || started.json["run_id"] == nil {
		t.Fatalf("start: %d %s", started.status, started.body)
	}
	thread, _ := started.json["thread"].(map[string]any)
	id := number(t, thread["id"])
	asstWaitRequests(t, api, requests+1)
	sent := s.do(http.MethodPost, threadPath(id, "/messages"), owner, map[string]any{"text": "queued once"})
	if sent.status != http.StatusAccepted || sent.json["queued_id"] == nil || sent.json["run_id"] != nil {
		t.Fatalf("send while running: %d %s", sent.status, sent.body)
	}
	free()
	view := waitThreadSettled(t, s, owner, id)
	users, last := 0, ""
	for _, item := range transcriptItems(view) {
		if item["kind"] == "user" && item["text"] == "queued once" {
			users++
		}
		if item["kind"] == "assistant" {
			last, _ = item["text"].(string)
		}
	}
	if users != 1 || last != "queued answer" {
		t.Fatalf("queued message delivered %d times, last reply %q: %s", users, last, view.body)
	}

	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("regenerated")))
	regen := s.do(http.MethodPost, threadPath(id, "/regenerate"), owner, nil)
	if regen.status != http.StatusAccepted || regen.json["run_id"] == nil {
		t.Fatalf("regenerate: %d %s", regen.status, regen.body)
	}
	view = waitThreadSettled(t, s, owner, id)
	items := transcriptItems(view)
	if tail := items[len(items)-1]; tail["kind"] != "assistant" || tail["text"] != "regenerated" {
		t.Fatalf("after regenerate: %s", view.body)
	}
	run := s.do(http.MethodGet, threadPath(id, "/runs/"+strconv.FormatInt(number(t, regen.json["run_id"]), 10)), owner, nil)
	if run.status != http.StatusOK || run.json["state"] == "running" || number(t, run.json["id"]) != number(t, regen.json["run_id"]) {
		t.Fatalf("run detail: %d %s", run.status, run.body)
	}
	expectAPI(t, s.do(http.MethodGet, threadPath(id, "/runs/999999"), owner, nil), http.StatusNotFound, "not_found")
	if work := s.do(http.MethodGet, threadPath(id, "/work"), owner, nil); work.status != http.StatusOK || work.json["needs"] == nil {
		t.Fatalf("work: %d %s", work.status, work.body)
	}
	if history := s.do(http.MethodGet, threadPath(id, "/history"), owner, nil); history.status != http.StatusOK || history.json["lines"] == nil {
		t.Fatalf("history: %d %s", history.status, history.body)
	}

	cfg := s.do(http.MethodGet, "/api/v1/assistant/configuration", owner, nil)
	efforts, _ := cfg.json["efforts"].([]any)
	if cfg.status != http.StatusOK || len(efforts) < 2 {
		t.Fatalf("configuration: %d %s", cfg.status, cfg.body)
	}
	if opts := s.do(http.MethodGet, "/api/v1/composer-options?model=claude-haiku-5-5&web_search=0", owner, nil); opts.status != http.StatusOK || opts.json["composer"] == nil {
		t.Fatalf("composer options: %d %s", opts.status, opts.body)
	}
	model := thread["model"]
	effort := efforts[0].(string)
	if effort == thread["effort"] {
		effort = efforts[1].(string)
	}
	expectAPI(t, s.do(http.MethodPatch, threadPath(id, "/options"), owner, map[string]any{"effort": "no-such-effort"}), http.StatusUnprocessableEntity, "invalid")

	etag := s.do(http.MethodGet, threadPath(id, ""), owner, nil).header.Get("ETag")
	if again := s.do(http.MethodGet, threadPath(id, ""), owner, nil, "If-None-Match", etag); etag == "" || again.status != http.StatusNotModified {
		t.Fatalf("transcript conditional read: %q %d", etag, again.status)
	}
	if changed := s.do(http.MethodPatch, threadPath(id, "/options"), owner, map[string]any{"effort": effort}); changed.status != http.StatusOK {
		t.Fatalf("options: %d %s", changed.status, changed.body)
	}
	view = s.do(http.MethodGet, threadPath(id, ""), owner, nil, "If-None-Match", etag)
	got, _ := view.json["thread"].(map[string]any)
	if view.status != http.StatusOK || got["effort"] != effort || got["model"] != model {
		t.Fatalf("options not applied or omitted model changed: %d %s", view.status, view.body)
	}

	var seq int64
	for _, item := range transcriptItems(view) {
		if item["kind"] == "assistant" {
			seq = number(t, item["seq"])
			break
		}
	}
	expectAPI(t, s.do(http.MethodPost, threadPath(id, "/branch"), owner, map[string]any{}), http.StatusUnprocessableEntity, "invalid")
	branched := s.do(http.MethodPost, threadPath(id, "/branch"), owner, map[string]any{"seq": seq})
	if branched.status != http.StatusCreated || branched.json["thread_id"] == nil {
		t.Fatalf("branch: %d %s", branched.status, branched.body)
	}
	child := number(t, branched.json["thread_id"])
	childView := s.do(http.MethodGet, threadPath(child, ""), owner, nil)
	childThread, _ := childView.json["thread"].(map[string]any)
	if childThread["effort"] != effort || childThread["model"] != model || number(t, childThread["branched_from_thread_id"]) != id {
		t.Fatalf("branch did not copy options: %s", childView.body)
	}

	if pinned := s.do(http.MethodPut, threadPath(id, "/pin"), owner, map[string]any{"pinned": true}); pinned.status != http.StatusOK {
		t.Fatalf("pin: %d %s", pinned.status, pinned.body)
	}
	expectAPI(t, s.do(http.MethodPut, threadPath(id, "/pin"), owner, map[string]any{}), http.StatusUnprocessableEntity, "invalid")
	if archivedReply := s.do(http.MethodPut, threadPath(child, "/archive"), owner, map[string]any{"archived": true}); archivedReply.status != http.StatusOK {
		t.Fatalf("archive: %d %s", archivedReply.status, archivedReply.body)
	}
	listed := s.do(http.MethodGet, "/api/v1/threads", owner, nil)
	archivedList := s.do(http.MethodGet, "/api/v1/threads?view=archived", owner, nil)
	first, _ := listed.json["items"].([]any)
	if len(first) == 0 || number(t, first[0].(map[string]any)["id"]) != id || first[0].(map[string]any)["pinned"] != true {
		t.Fatalf("pinned thread not first: %s", listed.body)
	}
	if arch, _ := archivedList.json["items"].([]any); len(arch) != 1 || number(t, arch[0].(map[string]any)["id"]) != child {
		t.Fatalf("archived list: %s", archivedList.body)
	}
	if moved := s.do(http.MethodPut, threadPath(id, "/project"), owner, map[string]any{"project_id": 999999}); moved.status != http.StatusNotFound {
		t.Fatalf("move to unknown project: %d %s", moved.status, moved.body)
	}
	if deleted := s.do(http.MethodDelete, threadPath(child, ""), owner, nil); deleted.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleted.status, deleted.body)
	}
	expectAPI(t, s.do(http.MethodGet, threadPath(child, ""), owner, nil), http.StatusNotFound, "not_found")

	now := float64(time.Now().Unix())
	for i := range 120 {
		if _, err := d.Write.Exec("INSERT INTO threads (kind, title, effort, created_at, updated_at) VALUES ('chat', ?, 'medium', ?, ?)", "bulk "+strconv.Itoa(i), now-1000-float64(i), now-1000-float64(i)); err != nil {
			t.Fatal(err)
		}
	}
	page := s.do(http.MethodGet, "/api/v1/threads?limit=100", owner, nil)
	pageItems, _ := page.json["items"].([]any)
	cursor, _ := page.json["next_cursor"].(string)
	if len(pageItems) != 100 || cursor == "" {
		t.Fatalf("first page: %d items, cursor %q", len(pageItems), cursor)
	}
	rest := s.do(http.MethodGet, "/api/v1/threads?limit=100&cursor="+cursor, owner, nil)
	restItems, _ := rest.json["items"].([]any)
	seen := map[int64]bool{}
	for _, item := range append(pageItems, restItems...) {
		seen[number(t, item.(map[string]any)["id"])] = true
	}
	want := asstCount(t, d, "SELECT COUNT(*) FROM threads WHERE kind = 'chat' AND archived = 0 AND deleted_at IS NULL")
	if len(seen) != want || want < 121 || rest.json["next_cursor"] != nil {
		t.Fatalf("paging saw %d distinct threads, want %d: %s", len(seen), want, rest.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/threads?view=archived&cursor="+cursor, owner, nil), http.StatusBadRequest, "bad_cursor")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/threads?view=bogus", owner, nil), http.StatusUnprocessableEntity, "invalid")

	home := s.do(http.MethodGet, "/api/v1/home", owner, nil)
	if home.status != http.StatusOK || home.json["date"] == "" || home.json["composer"] == nil || home.header.Get("ETag") == "" {
		t.Fatalf("home: %d %s", home.status, home.body)
	}
	if again := s.do(http.MethodGet, "/api/v1/home", owner, nil, "If-None-Match", home.header.Get("ETag")); again.status != http.StatusNotModified {
		t.Fatalf("home conditional read: %d", again.status)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/home?view=bogus", owner, nil), http.StatusUnprocessableEntity, "invalid")
	if about := s.do(http.MethodGet, "/api/v1/home/about?ref=nothing", owner, nil); about.status != http.StatusOK {
		t.Fatalf("about: %d %s", about.status, about.body)
	}
	today := s.do(http.MethodPost, "/api/v1/today", owner, nil)
	todayAgain := s.do(http.MethodPost, "/api/v1/today", owner, nil)
	if today.status != http.StatusOK || today.json["thread_id"] == nil || number(t, today.json["thread_id"]) != number(t, todayAgain.json["thread_id"]) {
		t.Fatalf("today ensure: %s / %s", today.body, todayAgain.body)
	}

	if ran := runApp(t, "settings", "set", "monthly_budget_usd", "1"); ran.code != cli.ExitOK {
		t.Fatalf("budget: %d %s", ran.code, ran.stderr)
	}
	asstSpendAt(t, d, inference.SourceLoop, 5, float64(time.Now().Unix()))
	requests = len(api.Requests())
	expectAPI(t, s.do(http.MethodPost, "/api/v1/threads", owner, map[string]any{"text": "x"}), http.StatusConflict, "budget_exceeded")
	expectAPI(t, s.do(http.MethodPost, threadPath(id, "/messages"), owner, map[string]any{"text": "x"}), http.StatusConflict, "budget_exceeded")
	expectAPI(t, s.do(http.MethodPost, threadPath(id, "/regenerate"), owner, nil), http.StatusConflict, "budget_exceeded")
	expectAPI(t, s.do(http.MethodPatch, threadPath(id, "/messages/latest"), owner, map[string]any{"text": "x"}), http.StatusConflict, "budget_exceeded")
	if extra := len(api.Requests()) - requests; extra != 0 {
		t.Fatalf("%d provider requests after refused sends", extra)
	}
}

func TestHTMLSendReplaysByKey(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	d := asstDB(t, dir)
	s := startAPISite(t, dir, api)
	owner := s.token("owner")
	client := s.browser()
	api.Push(asstReply("started"), asstReply("keyed reply"), asstReply("second key reply"))

	started := s.do(http.MethodPost, "/api/v1/threads", owner, map[string]any{"text": "hello", "web_search": false})
	if started.status != http.StatusCreated {
		t.Fatalf("start: %d %s", started.status, started.body)
	}
	thread, _ := started.json["thread"].(map[string]any)
	id := number(t, thread["id"])
	waitThreadSettled(t, s, owner, id)
	requests := len(api.Requests())

	send := func(key string) (int, string, http.Header) {
		t.Helper()
		body, contentType := asstMultipart(t, map[string]string{"text": "keyed note"}, nil)
		req, err := http.NewRequest(http.MethodPost, s.base+"/chat/"+strconv.FormatInt(id, 10)+"/messages", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("HX-Request", "true")
		req.Header.Set("Idempotency-Key", key)
		for _, c := range client.Jar.Cookies(req.URL) {
			if c.Name == web.CSRFCookie {
				req.Header.Set(web.CSRFHeader, c.Value)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		text, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(text), resp.Header
	}
	notes := func() int {
		return asstCount(t, d, "SELECT COUNT(*) FROM thread_messages WHERE thread_id = ? AND role = 'user' AND superseded = 0 AND content_json LIKE '%keyed note%'", id)
	}

	status, body, header := send("html-send-key-1")
	if status != http.StatusOK || header.Get("Atrium-Command-Id") == "" {
		t.Fatalf("first keyed send: %d %q %s", status, header.Get("Atrium-Command-Id"), body)
	}
	waitThreadSettled(t, s, owner, id)
	again, againBody, againHeader := send("html-send-key-1")
	if again != status || againBody != body || againHeader.Get("Atrium-Command-Id") != header.Get("Atrium-Command-Id") || againHeader.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("repeated keyed send: %d %q replayed=%q, want %d %q and the first response", again, againHeader.Get("Atrium-Command-Id"), againHeader.Get("Idempotent-Replayed"), status, header.Get("Atrium-Command-Id"))
	}
	waitThreadSettled(t, s, owner, id)
	if n := notes(); n != 1 {
		t.Fatalf("one key stored %d messages, want 1", n)
	}
	if sent := len(api.Requests()) - requests; sent != 1 {
		t.Fatalf("one key made %d provider requests, want 1", sent)
	}

	other, otherBody, otherHeader := send("html-send-key-2")
	if other != http.StatusOK || otherHeader.Get("Idempotent-Replayed") != "" || otherHeader.Get("Atrium-Command-Id") == header.Get("Atrium-Command-Id") {
		t.Fatalf("second key: %d %q %s", other, otherHeader.Get("Atrium-Command-Id"), otherBody)
	}
	waitThreadSettled(t, s, owner, id)
	if n := notes(); n != 2 {
		t.Fatalf("two keys stored %d messages, want 2", n)
	}
}
