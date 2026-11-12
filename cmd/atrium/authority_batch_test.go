package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/weather"
)

const forecastJSON = `{"latitude":10.65,"longitude":-61.5,"timezone":"America/Port_of_Spain",` +
	`"current":{"time":"2026-11-08T12:00","temperature_2m":31.5,"apparent_temperature":35.0,` +
	`"relative_humidity_2m":70,"precipitation":0.0,"weather_code":2,"wind_speed_10m":12.0,` +
	`"wind_direction_10m":90,"is_day":1},` +
	`"hourly":{"time":["2026-11-08T12:00","2026-11-08T13:00"],"temperature_2m":[31.5,32.0],` +
	`"precipitation_probability":[10,20],"weather_code":[2,3]},` +
	`"daily":{"time":["2026-11-08","2026-11-09"],"weather_code":[2,61],` +
	`"temperature_2m_max":[33.0,32.0],"temperature_2m_min":[24.0,23.0],` +
	`"precipitation_probability_max":[20,60],` +
	`"sunrise":["2026-11-08T06:00","2026-11-09T06:00"],` +
	`"sunset":["2026-11-08T17:45","2026-11-09T17:45"]}}`

func permissionProposals(t *testing.T, dir string) int {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM proposals WHERE kind = 'permission'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func findToolResult(t *testing.T, requests []anthropictest.Request, id string) map[string]any {
	t.Helper()
	for _, r := range requests {
		messages, _ := r.JSON(t)["messages"].([]any)
		for _, raw := range messages {
			m, _ := raw.(map[string]any)
			content, _ := m["content"].([]any)
			for _, b := range content {
				if entry, _ := b.(map[string]any); entry["type"] == "tool_result" && entry["tool_use_id"] == id {
					return entry
				}
			}
		}
	}
	return nil
}

func TestAuthorityBatchRefusesTheInternetAfterAMailRead(t *testing.T) {
	srv, pages := webPages(t)
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	start := warm(t, api)
	message := seedMail(t, dir)
	api.Push(
		anthropictest.Reply("tool_use",
			anthropictest.ToolUse("m1", "mail_read", map[string]any{"message_id": message}),
			anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if ran := runApp(t, "agent", "run", "--text", "read my mail then fetch "+srv.URL+"/a"); ran.code != cli.ExitOK {
		t.Fatalf("run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	if result := toolResultFor(t, requests[1], "m1"); result["is_error"] == true {
		t.Errorf("mail_read failed: %v", result)
	}
	refusal := toolResultFor(t, requests[1], "f1")
	if refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "web_fetch is not allowed in this thread by the permission table") {
		t.Errorf("web_fetch in the same response as a mail read returned %v, want a refusal", refusal)
	}
	if hits := pages.seen(); len(hits) != 0 {
		t.Errorf("the batch reached the web: %v", hits)
	}
	if n := permissionProposals(t, dir); n != 0 {
		t.Errorf("%d permission proposals, want none", n)
	}
	if marks := threadMarkSet(t, dir, latestThread(t, dir)); marks&int64(tool.PII) == 0 {
		t.Errorf("thread marks = %d, want the mail read's pii mark", marks)
	}
}

func TestQueuedMarkedReferenceAppliesToTheRestOfTheBatch(t *testing.T) {
	srv, pages := webPages(t)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	message := seedMail(t, dir)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("m1", "mail_read", map[string]any{"message_id": message})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("read")))
	if ran := runApp(t, "agent", "run", "--text", "read my mail"); ran.code != cli.ExitOK {
		t.Fatalf("source run: %d %s", ran.code, ran.stderr)
	}
	source := latestThread(t, dir)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("started")))
	if ran := runApp(t, "agent", "run", "--text", "start"); ran.code != cli.ExitOK {
		t.Fatalf("reader run: %d %s", ran.code, ran.stderr)
	}
	reader := latestThread(t, dir)

	var base string
	var client *http.Client
	queued := make(chan struct{}, 1)
	forecast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		form := url.Values{"text": {fmt.Sprintf("@thread/%d", source)}}
		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/chat/%d/messages", base, reader), strings.NewReader(form.Encode()))
		if err != nil {
			t.Errorf("queue reference: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range client.Jar.Cookies(req.URL) {
			if c.Name == "atrium_csrf" {
				req.Header.Set("X-CSRF-Token", c.Value)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("queue reference: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= http.StatusBadRequest {
			t.Errorf("queue reference = %d: %s", resp.StatusCode, body)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		queued <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(forecastJSON))
	}))
	t.Cleanup(forecast.Close)
	setEnv(t, "ATRIUM_HOST_CONFIG", weatherHost(t, dir, forecast.URL+"/v1/forecast", forecast.URL+"/v1/search"))
	seedSettings(t, map[string]string{weather.KeyLatitude: "10.65", weather.KeyLongitude: "-61.5", weather.KeyLocationName: "Port of Spain"})

	base, _, _ = startServeApp(t, freshApp(f), &lockedBuffer{})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, "GET", base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}

	start := len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use",
			anthropictest.ToolUse("w1", "weather", map[string]any{}),
			anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if status, body, _ := fetch(t, client, "POST", fmt.Sprintf("%s/chat/%d/messages", base, reader), url.Values{"text": {"weather then read " + srv.URL + "/a"}}); status >= http.StatusBadRequest {
		t.Fatalf("POST message = %d: %s", status, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	waitRunState(t, d, reader, "done")
	requests := api.Requests()[start:]
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	if result := toolResultFor(t, requests[1], "w1"); result["is_error"] == true {
		t.Errorf("weather failed: %v", result)
	}
	refusal := toolResultFor(t, requests[1], "f1")
	if refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "web_fetch is not allowed in this thread by the permission table") {
		t.Errorf("web_fetch after the queued reference returned %v, want a refusal", refusal)
	}
	if hits := pages.seen(); len(hits) != 0 {
		t.Errorf("the batch reached the web: %v", hits)
	}
	select {
	case <-queued:
	default:
		t.Error("the weather tool did not queue the reference")
	}
	if marks := threadMarkSet(t, dir, reader); marks&int64(tool.PII) == 0 {
		t.Errorf("reader marks = %d, want the reference's pii mark", marks)
	}
	var pending int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM thread_queue WHERE thread_id = ? AND consumed_at IS NULL", reader).Scan(&pending); err != nil || pending != 0 {
		t.Errorf("queued messages = %d, %v, want the reference consumed", pending, err)
	}
}

func TestServerWebSearchResultMarksBeforeTheSameResponseClientTool(t *testing.T) {
	srv, pages := webPages(t)
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	start := warm(t, api)
	blocks := append(anthropictest.WebSearch("ws1", "library hours", anthropictest.SearchResult{URL: srv.URL + "/b", Title: "B"}),
		anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"}))
	api.Push(anthropictest.Reply("pause_turn", blocks...))
	if ran := runApp(t, "agent", "run", "--text", "search then read "+srv.URL+"/a"); ran.code != cli.ExitOK {
		t.Fatalf("run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 1 {
		t.Fatalf("%d model requests, want 1 while the run parks", len(requests))
	}
	if hits := pages.seen(); len(hits) != 0 {
		t.Errorf("the client tool in the search response reached the web: %v", hits)
	}
	thread := latestThread(t, dir)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	proposal, _ := waitParkedPermission(t, d, thread)
	var title string
	if err := d.Read.QueryRow("SELECT title FROM proposals WHERE id = ?", proposal).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(title, "web_fetch") {
		t.Errorf("the parked request %q is not the web_fetch", title)
	}
	if marks := threadMarkSet(t, dir, thread); marks&int64(tool.Untrusted) == 0 {
		t.Errorf("thread marks = %d, want the search's untrusted mark", marks)
	}
}

func TestLookupMarksApplyToTheNextInternetCall(t *testing.T) {
	srv, pages := webPages(t)
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	start := warm(t, api)
	message := seedMail(t, dir)
	api.Push(
		anthropictest.Reply("tool_use",
			anthropictest.ToolUse("l1", "lookup", map[string]any{
				"source": "mail", "question": "What is the code?",
				"fields": []any{map[string]any{"name": "code", "type": "text"}},
			}),
			anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("m1", "mail_read", map[string]any{"message_id": message})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "report", map[string]any{"answer": map[string]any{"code": "4242"}})),
		anthropictest.Reply("end_turn", anthropictest.Text("ok")),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if ran := runApp(t, "agent", "run", "--text", "look up the code then read "+srv.URL+"/a"); ran.code != cli.ExitOK {
		t.Fatalf("run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 5 {
		t.Fatalf("%d model requests, want 5", len(requests))
	}
	if result := toolResultFor(t, requests[4], "l1"); result["is_error"] == true || !strings.Contains(fmt.Sprint(result["content"]), "4242") {
		t.Errorf("lookup returned %v, want the code", result)
	}
	refusal := toolResultFor(t, requests[4], "f1")
	if refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "web_fetch is not allowed in this thread by the permission table") {
		t.Errorf("web_fetch after lookup returned %v, want a refusal", refusal)
	}
	if hits := pages.seen(); len(hits) != 0 {
		t.Errorf("the batch reached the web: %v", hits)
	}
	if marks := threadMarkSet(t, dir, latestThread(t, dir)); marks&int64(tool.PII) == 0 {
		t.Errorf("thread marks = %d, want the lookup's pii mark", marks)
	}
}

func TestApprovedPermissionExecutesOnceAndDoesNotReplay(t *testing.T) {
	srv, pages := webPages(t)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	thread := latestThread(t, dir)
	base, _, _ := startServeApp(t, freshApp(f), &lockedBuffer{})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, "GET", base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}
	start := len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("f2", "web_fetch", map[string]any{"url": srv.URL + "/b"})),
		anthropictest.Reply("end_turn", anthropictest.Text("read both")),
	)
	text := "read " + srv.URL + "/a then " + srv.URL + "/b"
	if status, body, _ := fetch(t, client, "POST", fmt.Sprintf("%s/chat/%d/messages", base, thread), url.Values{"text": {text}}); status >= http.StatusBadRequest {
		t.Fatalf("POST message = %d: %s", status, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	proposal, _ := waitParkedPermission(t, d, thread)
	if hits := pages.seen(); !slices.Equal(hits, []string{"/a"}) {
		t.Fatalf("page fetches before approval = %v, want /a only", hits)
	}
	if code, body := hxFetch(t, client, http.MethodPost, base+fmt.Sprintf("/proposals/%d/approve", proposal), url.Values{"all": {"1"}}); code != http.StatusOK {
		t.Fatalf("approve = %d\n%s", code, body)
	}
	waitRunState(t, d, thread, "done")
	if hits := pages.seen(); !slices.Equal(hits, []string{"/a", "/b"}) {
		t.Fatalf("page fetches after approval = %v, want /a then /b once", hits)
	}
	requests := api.Requests()[start:]
	last := requests[len(requests)-1]
	if n := strings.Count(string(last.Body), pageBText); n != 1 {
		t.Errorf("the approved page reached the model %d times, want 1", n)
	}
	code, body := hxFetch(t, client, http.MethodPost, base+fmt.Sprintf("/proposals/%d/approve", proposal), url.Values{"all": {"1"}})
	if code != http.StatusOK || !strings.Contains(body, "is not open") {
		t.Errorf("the replayed approval = %d: %s, want a refusal", code, body)
	}
	if hits := pages.seen(); !slices.Equal(hits, []string{"/a", "/b"}) {
		t.Errorf("page fetches after the replay = %v, want /a then /b once", hits)
	}
	var status string
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", proposal).Scan(&status); err != nil || status != "applied" {
		t.Errorf("proposal status = %q, %v, want applied", status, err)
	}
}

func TestApprovalDeniedByANewMarkPerformsNothing(t *testing.T) {
	srv, pages := webPages(t)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	thread := latestThread(t, dir)
	base, _, _ := startServeApp(t, freshApp(f), &lockedBuffer{})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, "GET", base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}
	start := len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_threads", map[string]any{"query": "warm"})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if status, body, _ := fetch(t, client, "POST", fmt.Sprintf("%s/chat/%d/messages", base, thread), url.Values{"text": {"find the warm thread then read " + srv.URL + "/a"}}); status >= http.StatusBadRequest {
		t.Fatalf("POST message = %d: %s", status, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	proposal, _ := waitParkedPermission(t, d, thread)
	if hits := pages.seen(); len(hits) != 0 {
		t.Fatalf("page fetches before the decision = %v, want none", hits)
	}
	if _, err := d.Write.Exec("UPDATE threads SET tainted_at = COALESCE(tainted_at, ?), marks = marks | ? WHERE id = ?",
		float64(time.Now().UnixNano())/1e9, int64(tool.PII), thread); err != nil {
		t.Fatal(err)
	}
	if code, body := hxFetch(t, client, http.MethodPost, base+fmt.Sprintf("/proposals/%d/approve", proposal), url.Values{"all": {"1"}}); code != http.StatusOK {
		t.Fatalf("approve = %d\n%s", code, body)
	}
	waitRunState(t, d, thread, "done")
	if hits := pages.seen(); len(hits) != 0 {
		t.Errorf("the approved fetch ran after a denying mark arrived: %v", hits)
	}
	requests := api.Requests()[start:]
	refusal := toolResultFor(t, requests[len(requests)-1], "f1")
	if refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "web_fetch is not allowed in this thread by the permission table") {
		t.Errorf("the approved fetch returned %v, want a refusal", refusal)
	}
	if marks := threadMarkSet(t, dir, thread); marks&int64(tool.PII) == 0 {
		t.Errorf("reader marks = %d, want the pii mark that arrived while parked", marks)
	}
}

func TestUnattendedRunsAndSubagentsCannotParkOrEscape(t *testing.T) {
	t.Run("cron", func(t *testing.T) {
		srv, pages := webPages(t)
		dir, api, f := moduleFixture(t, flagsExcept("", ""))
		warm(t, api)
		api.Push(
			anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_threads", map[string]any{"query": "warm"})),
			anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "schedule_create", map[string]any{
				"name": "Batch check", "prompt": "Check " + srv.URL + "/a for news.", "schedule": "daily 09:00",
			})),
			anthropictest.Reply("end_turn", anthropictest.Text("scheduled")),
		)
		if ran := runApp(t, "agent", "run", "--text", "find a slot and schedule a check"); ran.code != cli.ExitOK {
			t.Fatalf("writer run: %d %s", ran.code, ran.stderr)
		}
		writer := latestThread(t, dir)
		if marks := threadMarkSet(t, dir, writer); marks != int64(tool.Messages) {
			t.Fatalf("writer marks = %d, want messages", marks)
		}
		d := mustConnect(t, filepath.Join(dir, "atrium.db"))
		var job int64
		if err := d.Read.QueryRow("SELECT id FROM jobs WHERE name = 'Batch check'").Scan(&job); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Write.Exec("UPDATE jobs SET next_due = 0 WHERE id = ?", job); err != nil {
			t.Fatal(err)
		}
		start := len(api.Requests())
		api.Push(
			anthropictest.Reply("tool_use", anthropictest.ToolUse("n1", "notify", map[string]any{"title": "Batch check", "body": "Nothing new"})),
			anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})),
			anthropictest.Reply("end_turn", anthropictest.Text("checked")),
		)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan appRun, 1)
		go func() { done <- runAppContext(t, f, ctx, "agent", "scheduler") }()
		var thread int64
		for deadline := time.Now().Add(20 * time.Second); ; {
			err := d.Read.QueryRow("SELECT thread_id FROM job_runs WHERE job_id = ? AND thread_id IS NOT NULL ORDER BY id DESC LIMIT 1", job).Scan(&thread)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatalf("the scheduled run never started: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		waitRunState(t, d, thread, "done")
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("the scheduler ignored cancellation")
		}
		requests := api.Requests()[start:]
		if result := toolResultFor(t, requests[len(requests)-1], "n1"); result["is_error"] == true {
			t.Errorf("notify failed in the cron run: %v", result)
		}
		refusal := toolResultFor(t, requests[len(requests)-1], "f1")
		if refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "not allowed in this thread by the permission table") {
			t.Errorf("cron web_fetch returned %v, want a refusal", refusal)
		}
		if hits := pages.seen(); len(hits) != 0 {
			t.Errorf("the cron run reached the web: %v", hits)
		}
		if n := permissionProposals(t, dir); n != 0 {
			t.Errorf("%d permission proposals, want none", n)
		}
	})

	t.Run("subagent", func(t *testing.T) {
		srv, pages := webPages(t)
		dir, api, _ := moduleFixture(t, flagsExcept("", ""))
		if made := runApp(t, "agent", "subagents", "set", "batch", "--description", "Batch test reader",
			"--prompt", "Read and report.", "--tool", "task_list", "--tool", "web_fetch"); made.code != cli.ExitOK {
			t.Fatalf("subagents set: %d %s", made.code, made.stderr)
		}
		warm(t, api)
		api.Push(
			anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_threads", map[string]any{"query": "warm"})),
			anthropictest.Reply("tool_use", anthropictest.ToolUse("t1", "task_add", map[string]any{"title": "Batch task"})),
			anthropictest.Reply("end_turn", anthropictest.Text("noted")),
		)
		if ran := runApp(t, "agent", "run", "--text", "find a slot and remind me"); ran.code != cli.ExitOK {
			t.Fatalf("writer run: %d %s", ran.code, ran.stderr)
		}
		writer := latestThread(t, dir)
		if marks := threadMarkSet(t, dir, writer); marks != int64(tool.Messages) {
			t.Fatalf("writer marks = %d, want messages", marks)
		}
		start := len(api.Requests())
		api.Push(
			anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "research", map[string]any{"role": "batch", "task": "read " + srv.URL + "/a"})),
			anthropictest.Reply("tool_use", anthropictest.ToolUse("m1", "mail_read", map[string]any{"message_id": 1})),
			anthropictest.Reply("tool_use", anthropictest.ToolUse("l1", "task_list", map[string]any{})),
			anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})),
			anthropictest.Reply("end_turn", anthropictest.Text("digest")),
			anthropictest.Reply("end_turn", anthropictest.Text("done")),
		)
		if ran := runApp(t, "agent", "run", "--text", "research the task then read "+srv.URL+"/a"); ran.code != cli.ExitOK {
			t.Fatalf("research run: %d %s", ran.code, ran.stderr)
		}
		requests := api.Requests()[start:]
		if len(requests) != 6 {
			t.Fatalf("%d model requests, want 6", len(requests))
		}
		escaped := findToolResult(t, requests, "m1")
		if escaped == nil || escaped["is_error"] != true || !strings.Contains(fmt.Sprint(escaped["content"]), "unknown tool mail_read") {
			t.Errorf("the child's out-of-allow-list call returned %v, want an unknown-tool refusal", escaped)
		}
		refusal := findToolResult(t, requests, "f1")
		if refusal == nil || refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "not allowed in this thread by the permission table") {
			t.Errorf("the child's web_fetch returned %v, want a refusal", refusal)
		}
		if hits := pages.seen(); len(hits) != 0 {
			t.Errorf("the subagent reached the web: %v", hits)
		}
		if n := permissionProposals(t, dir); n != 0 {
			t.Errorf("%d permission proposals, want none", n)
		}
		if marks := threadMarkSet(t, dir, latestThread(t, dir)); marks&int64(tool.Messages) == 0 {
			t.Errorf("parent marks = %d, want the child's messages mark", marks)
		}
	})
}

func TestParkedBatchKeepsCompletedResultsAndDoesNotReplayThem(t *testing.T) {
	srv, pages := webPages(t)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	thread := latestThread(t, dir)
	base, _, _ := startServeApp(t, freshApp(f), &lockedBuffer{})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, "GET", base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}
	start := len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use",
			anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"}),
			anthropictest.ToolUse("f2", "web_fetch", map[string]any{"url": srv.URL + "/b"})),
		anthropictest.Reply("end_turn", anthropictest.Text("read both")),
	)
	text := "read " + srv.URL + "/a then " + srv.URL + "/b"
	if status, body, _ := fetch(t, client, "POST", fmt.Sprintf("%s/chat/%d/messages", base, thread), url.Values{"text": {text}}); status >= http.StatusBadRequest {
		t.Fatalf("POST message = %d: %s", status, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	proposal, _ := waitParkedPermission(t, d, thread)
	if hits := pages.seen(); !slices.Equal(hits, []string{"/a"}) {
		t.Fatalf("page fetches before approval = %v, want /a only", hits)
	}
	if n := permissionProposals(t, dir); n != 1 {
		t.Fatalf("%d permission proposals before approval, want 1", n)
	}
	if code, body := hxFetch(t, client, http.MethodPost, base+fmt.Sprintf("/proposals/%d/approve", proposal), url.Values{"all": {"1"}}); code != http.StatusOK {
		t.Fatalf("approve = %d\n%s", code, body)
	}
	waitRunState(t, d, thread, "done")
	if hits := pages.seen(); !slices.Equal(hits, []string{"/a", "/b"}) {
		t.Fatalf("page fetches after approval = %v, want /a then /b once", hits)
	}
	if n := permissionProposals(t, dir); n != 1 {
		t.Errorf("%d permission proposals after approval, want 1: the completed call was replayed", n)
	}
	requests := api.Requests()[start:]
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	kept := toolResultFor(t, requests[1], "f1")
	if kept["is_error"] == true || !strings.Contains(fmt.Sprint(kept["content"]), `"result_id"`) {
		t.Errorf("the completed fetch returned %v, want its earlier stored result", kept)
	}
	approved := toolResultFor(t, requests[1], "f2")
	if approved["is_error"] == true || !strings.Contains(fmt.Sprint(approved["content"]), pageBText) {
		t.Errorf("the approved fetch returned %v, want page B", approved)
	}
}

func TestCLIApprovalReauthorizesTheCurrentPermissions(t *testing.T) {
	qb, qbURL := mnpNewQbit(t)
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	mnpMediaRoot(t, dir)
	setEnv(t, "ATRIUM_HOST_CONFIG", qbitHost(t, dir, qbURL))
	start := warm(t, api)
	const hash = "dddddddddddddddddddddddddddddddddddddddd"
	magnet := mediaMagnet(hash, "Lanterns.S01E12")
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("d1", "media_add_magnet", map[string]any{"magnet": magnet})),
		anthropictest.Reply("end_turn", anthropictest.Text("asked")),
	)
	if ran := runApp(t, "agent", "run", "--text", "add "+magnet); ran.code != cli.ExitOK {
		t.Fatalf("run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	if result := toolResultFor(t, requests[1], "d1"); result["is_error"] == true {
		t.Fatalf("media_add_magnet failed: %v", result)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var id int64
	if err := d.Read.QueryRow("SELECT MAX(id) FROM proposals WHERE kind = 'media_add_magnet'").Scan(&id); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	if ran := runApp(t, "settings", "set", "tool_media_add_magnet_enabled", "false"); ran.code != cli.ExitOK {
		t.Fatalf("disable the tool: %d %s", ran.code, ran.stderr)
	}
	if ran := runApp(t, "proposals", "approve", fmt.Sprint(id)); ran.code == cli.ExitOK {
		t.Fatalf("the CLI approved a disabled tool: %s", ran.stdout)
	}
	var status, result string
	if err := d.Read.QueryRow("SELECT status, COALESCE(result, '') FROM proposals WHERE id = ?", id).Scan(&status, &result); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || !strings.Contains(result, "media_add_magnet is not allowed in this thread by the permission table") {
		t.Errorf("proposal = %s %s, want a failed re-authorization", status, result)
	}
	if adds := qb.count("add"); adds != 0 {
		t.Errorf("the CLI approval reached qBittorrent %d times, want none", adds)
	}
}
