package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

type appFrame struct {
	event string
	data  map[string]any
}

func appFrames(t *testing.T, resp *http.Response) <-chan appFrame {
	t.Helper()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("app event stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	out := make(chan appFrame, 64)
	go func() {
		defer close(out)
		scan := bufio.NewScanner(resp.Body)
		var f appFrame
		for scan.Scan() {
			line := scan.Text()
			switch {
			case strings.HasPrefix(line, "id:"):
				f.event = "unexpected id: " + line
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f.data)
			case line == "" && f.event != "":
				out <- f
				f = appFrame{}
			}
		}
	}()
	return out
}

func nextAppFrame(t *testing.T, frames <-chan appFrame, event string) map[string]any {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("the app event stream closed before %s", event)
			}
			if f.event == event {
				return f.data
			}
			if f.event != "change" {
				t.Fatalf("unexpected frame %q %v while waiting for %s", f.event, f.data, event)
			}
		case <-timeout:
			t.Fatalf("no %s event", event)
		}
	}
}

func syncedRevisions(t *testing.T, data map[string]any) map[string]int64 {
	t.Helper()
	topics, ok := data["topics"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("sync data: %v", data)
	}
	out := map[string]int64{}
	for _, raw := range topics {
		topic, _ := raw.(map[string]any)
		name, _ := topic["topic"].(string)
		if len(topic) != 2 || name == "" {
			t.Fatalf("sync topic carries more than topic and revision: %v", topic)
		}
		out[name] = number(t, topic["revision"])
	}
	return out
}

func waitAppChange(t *testing.T, frames <-chan appFrame, topic string, after int64) int64 {
	t.Helper()
	for {
		data := nextAppFrame(t, frames, "change")
		if len(data) != 2 {
			t.Fatalf("change carries more than topic and revision: %v", data)
		}
		if data["topic"] != topic {
			continue
		}
		revision := number(t, data["revision"])
		if revision <= after {
			t.Fatalf("%s change did not advance: %d after %d", topic, revision, after)
		}
		return revision
	}
}

func (s *streamSite) openApp() *http.Response {
	s.t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.base+"/api/v1/events", nil)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestAppEventStreamSignalsChangesWithinItsOwnLimit(t *testing.T) {
	t.Parallel()
	s := newStreamSite(t)
	first := appFrames(t, s.openApp())
	synced := syncedRevisions(t, nextAppFrame(t, first, "sync"))
	for _, topic := range []string{"tasks", "assistant", "settings", "review"} {
		if _, ok := synced[topic]; !ok {
			t.Fatalf("sync has no %s topic: %v", topic, synced)
		}
	}

	added := s.do(http.MethodPost, "/api/v1/tasks", s.token, map[string]any{"title": "Water the plants"})
	if added.status != http.StatusCreated {
		t.Fatalf("add task: %d %s", added.status, added.body)
	}
	tasks := waitAppChange(t, first, "tasks", synced["tasks"])

	second := appFrames(t, s.openApp())
	if again := syncedRevisions(t, nextAppFrame(t, second, "sync")); again["tasks"] != tasks {
		t.Fatalf("a new stream synced tasks at %d, the change said %d", again["tasks"], tasks)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/events", s.token, nil), http.StatusTooManyRequests, "rate_limited")

	added = s.do(http.MethodPost, "/api/v1/tasks", s.token, map[string]any{"title": "Water the plants again"})
	if added.status != http.StatusCreated {
		t.Fatalf("add task: %d %s", added.status, added.body)
	}
	waitAppChange(t, first, "tasks", tasks)
	waitAppChange(t, second, "tasks", tasks)

	free := s.hold()
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("held")))
	threadID, runID := s.start("limits")
	waitRunning(t, s.d, runID)
	for i := range 4 {
		if resp := s.open(threadID, runID); resp.StatusCode != http.StatusOK {
			t.Fatalf("run stream %d beside two app streams: %d", i+1, resp.StatusCode)
		}
	}
	expectAPI(t, s.do(http.MethodGet, fmt.Sprintf("/api/v1/threads/%d/runs/%d/events", threadID, runID), s.token, nil), http.StatusTooManyRequests, "rate_limited")
	free()

	web := newWebClient(t, s.base, nil)
	web.session("local")
	resp, err := web.http.Do(web.request(http.MethodGet, "/web/v1/events", nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	browser := appFrames(t, resp)
	syncedRevisions(t, nextAppFrame(t, browser, "sync"))
	expectAPI(t, web.do(http.MethodGet, "/web/v1/events", nil, "Atrium-Client", ""), http.StatusForbidden, "cross_origin")
	made := web.do(http.MethodPost, "/web/v1/tasks", map[string]any{"title": "Buy seeds"})
	if made.status != http.StatusCreated {
		t.Fatalf("add task on /web/v1: %d %s", made.status, made.body)
	}
	waitAppChange(t, browser, "tasks", tasks)
}

func TestFreshDatabaseSendsEmptyCollectionsAsArrays(t *testing.T) {
	t.Parallel()
	s := newStreamSite(t)
	array := func(what string, v any) []any {
		t.Helper()
		list, ok := v.([]any)
		if !ok {
			t.Fatalf("%s = %#v, want an array", what, v)
		}
		return list
	}
	if origins := array("auth origins", apiData(t, s.do(http.MethodGet, "/api/v1/auth", s.token, nil), http.StatusOK)["origins"]); len(origins) != 0 {
		t.Fatalf("auth origins without OIDC = %v", origins)
	}
	review := apiData(t, s.do(http.MethodGet, "/api/v1/review/items", s.token, nil), http.StatusOK)
	for _, field := range []string{"items", "groups"} {
		if list := array("review "+field, review[field]); len(list) != 0 {
			t.Fatalf("review %s on a fresh database = %v", field, list)
		}
	}
	for _, raw := range array("connections", apiData(t, s.do(http.MethodGet, "/api/v1/connections", s.token, nil), http.StatusOK)["connections"]) {
		c, _ := raw.(map[string]any)
		array(fmt.Sprintf("%v credentials", c["id"]), c["credentials"])
		array(fmt.Sprintf("%v capabilities", c["id"]), c["capabilities"])
	}

	scan := bufio.NewScanner(s.openApp().Body)
	event := ""
	for scan.Scan() {
		line := scan.Text()
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			event = name
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || event != "sync" {
			continue
		}
		var frame struct {
			Topics []struct {
				Topic    string `json:"topic"`
				Revision *int64 `json:"revision"`
			} `json:"topics"`
		}
		if !strings.HasPrefix(data, `{"topics":[`) || json.Unmarshal([]byte(data), &frame) != nil || len(frame.Topics) == 0 {
			t.Fatalf("sync data is not a JSON object with a topics array: %s", data)
		}
		for _, topic := range frame.Topics {
			if topic.Topic == "" || topic.Revision == nil {
				t.Fatalf("sync topic: %s", data)
			}
		}
		return
	}
	t.Fatalf("the app event stream ended without a sync frame: %v", scan.Err())
}
