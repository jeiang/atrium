package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/db"
)

func lastUserText(t *testing.T, r anthropictest.Request) string {
	t.Helper()
	messages, _ := r.JSON(t)["messages"].([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		m, _ := messages[i].(map[string]any)
		if m["role"] != "user" {
			continue
		}
		var parts []string
		content, _ := m["content"].([]any)
		for _, raw := range content {
			b, _ := raw.(map[string]any)
			if b["type"] == "text" {
				if text, ok := b["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func threadMessages(t *testing.T, dir string, thread int64) int {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM thread_messages WHERE thread_id = ? AND superseded = 0", thread).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitIdle(t *testing.T, dir string, thread int64) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var active int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM runs WHERE thread_id = ? AND state = ?", thread, "running").Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the thread did not finish its run")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReferenceBringsAThreadVerbatimWithItsMarks(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	message := seedMail(t, dir)

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("m1", "mail_read", map[string]any{"message_id": message})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("the code is 4242")))
	if ran := runApp(t, "agent", "run", "--text", "read my latest mail"); ran.code != 0 {
		t.Fatalf("source run: %d %s", ran.code, ran.stderr)
	}
	source := latestThread(t, dir)
	sourceMarks := threadMarkSet(t, dir, source)
	if sourceMarks == 0 {
		t.Fatal("source thread carries no marks")
	}

	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("started")))
	if ran := runApp(t, "agent", "run", "--text", "start"); ran.code != 0 {
		t.Fatalf("reader run: %d %s", ran.code, ran.stderr)
	}
	reader := latestThread(t, dir)

	var others []int64
	for i := range 3 {
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text(fmt.Sprintf("extra %d", i))))
		if ran := runApp(t, "agent", "run", "--text", fmt.Sprintf("extra %d", i)); ran.code != 0 {
			t.Fatalf("extra run: %d %s", ran.code, ran.stderr)
		}
		others = append(others, latestThread(t, dir))
	}

	base, _, _ := startServeApp(t, freshApp(f), &lockedBuffer{})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, "GET", base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}

	before := len(api.Requests())
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("noted")))
	status, body, _ := fetch(t, client, "POST", fmt.Sprintf("%s/chat/%d/messages", base, reader), url.Values{"text": {fmt.Sprintf("see @thread/%d", source)}})
	if status >= http.StatusBadRequest {
		t.Fatalf("POST reference = %d: %s", status, body)
	}
	for deadline := time.Now().Add(20 * time.Second); len(api.Requests()) == before; {
		if time.Now().After(deadline) {
			t.Fatal("the reference run sent no model request")
		}
		time.Sleep(20 * time.Millisecond)
	}
	requests := api.Requests()
	request := requests[len(requests)-1]
	text := lastUserText(t, request)
	for _, want := range []string{
		fmt.Sprintf(`Referenced thread "read my latest mail" (/chat/%d)`, source),
		"read my latest mail",
		"the code is 4242",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the operator message lacks %q: %s", want, text)
		}
	}
	if marks := threadMarkSet(t, dir, reader); marks != sourceMarks {
		t.Errorf("reader marks = %d, want the source's %d", marks, sourceMarks)
	}
	assertInternet(t, "the run that referenced a marked thread", requestTools(t, request), false)
	assertOwn(t, "the run that referenced a marked thread", requestTools(t, request), true)

	before = len(api.Requests())
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("again")))
	if status, body, _ := fetch(t, client, "POST", fmt.Sprintf("%s/chat/%d/messages", base, reader), url.Values{"text": {"and again"}}); status >= http.StatusBadRequest {
		t.Fatalf("POST follow-up = %d: %s", status, body)
	}
	for deadline := time.Now().Add(20 * time.Second); len(api.Requests()) == before; {
		if time.Now().After(deadline) {
			t.Fatal("the follow-up run sent no model request")
		}
		time.Sleep(20 * time.Millisecond)
	}
	requests = api.Requests()
	assertInternet(t, "a later run of the marked reader", requestTools(t, requests[len(requests)-1]), false)
	assertOwn(t, "a later run of the marked reader", requestTools(t, requests[len(requests)-1]), true)

	waitIdle(t, dir, reader)
	count := threadMessages(t, dir, reader)
	tooMany := fmt.Sprintf("@thread/%d @thread/%d @thread/%d @thread/%d", source, others[0], others[1], others[2])
	status, body, _ = fetch(t, client, "POST", fmt.Sprintf("%s/chat/%d/messages", base, reader), url.Values{"text": {tooMany}})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("POST four references = %d, want 422: %s", status, body)
	}
	if after := threadMessages(t, dir, reader); after != count {
		t.Errorf("a refused message stored %d messages, want %d", after-count, 0)
	}
	var stored int
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM thread_messages WHERE thread_id = ? AND content_json LIKE '%\"type\":\"reference\"%'", reader).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	if stored != 1 {
		t.Errorf("%d stored reference blocks, want 1", stored)
	}
	if raw := string(request.Body); strings.Contains(raw, `"type":"reference"`) {
		t.Error("the stored reference block reached the model unexpanded")
	}
}

func TestAPISendReferenceStoresTheHTMLContext(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("the code is 4242")))
	if ran := runApp(t, "agent", "run", "--text", "remember the code"); ran.code != 0 {
		t.Fatalf("source run: %d %s", ran.code, ran.stderr)
	}
	source := latestThread(t, dir)
	readers := make([]int64, 2)
	for i := range readers {
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("started")))
		if ran := runApp(t, "agent", "run", "--text", "start"); ran.code != 0 {
			t.Fatalf("reader run: %d %s", ran.code, ran.stderr)
		}
		readers[i] = latestThread(t, dir)
	}
	s := startAPISite(t, dir, api)
	text := fmt.Sprintf("see @thread/%d", source)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("noted")))
	if status, body, _ := fetch(t, s.browser(), "POST", fmt.Sprintf("%s/chat/%d/messages", s.base, readers[0]), url.Values{"text": {text}}); status >= http.StatusBadRequest {
		t.Fatalf("HTML send = %d: %s", status, body)
	}
	waitIdle(t, dir, readers[0])
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("noted")))
	if sent := s.do(http.MethodPost, fmt.Sprintf("/api/v1/threads/%d/messages", readers[1]), s.token("owner"), map[string]any{"text": text}); sent.status != http.StatusAccepted {
		t.Fatalf("API send = %d %s", sent.status, sent.body)
	}
	waitIdle(t, dir, readers[1])
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	stored := make([]string, len(readers))
	for i, reader := range readers {
		if err := d.Read.QueryRow("SELECT content_json FROM thread_messages WHERE thread_id = ? AND role = 'user' AND superseded = 0 ORDER BY seq DESC LIMIT 1", reader).Scan(&stored[i]); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(stored[0], `"type":"reference"`) || stored[0] != stored[1] {
		t.Fatalf("API send stored %s, HTML send stored %s", stored[1], stored[0])
	}
}
