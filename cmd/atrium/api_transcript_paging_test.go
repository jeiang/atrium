package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func TestTranscriptPagesReturnEveryItemOnceInOrder(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	s := startAPISite(t, dir, api)
	owner := s.token("owner")
	api.Push(
		anthropictest.Reply("end_turn", anthropictest.Thinking("weighing it", "sig-1"), anthropictest.Text("thought it through")),
		anthropictest.Reply("tool_use", anthropictest.Thinking("two keys to read", "sig-2"), anthropictest.Text("reading both"),
			anthropictest.ToolUse("p1", "profile_get", map[string]any{"key": "home"}),
			anthropictest.ToolUse("p2", "profile_get", map[string]any{"key": "work"})),
		anthropictest.Reply("end_turn", anthropictest.Thinking("both read", "sig-3"), anthropictest.Text("first part"),
			anthropictest.Thinking("one more thought", "sig-4"), anthropictest.Text("second part")),
	)

	started := s.do(http.MethodPost, "/api/v1/threads", owner, map[string]any{"text": "think first", "web_search": false})
	if started.status != http.StatusCreated {
		t.Fatalf("start: %d %s", started.status, started.body)
	}
	thread, _ := started.json["thread"].(map[string]any)
	id := number(t, thread["id"])
	waitThreadSettled(t, s, owner, id)
	if sent := s.do(http.MethodPost, threadPath(id, "/messages"), owner, map[string]any{"text": "now read two keys"}); sent.status != http.StatusAccepted {
		t.Fatalf("send: %d %s", sent.status, sent.body)
	}
	full := waitThreadSettled(t, s, owner, id)
	want := encodedItems(t, transcriptItems(full))
	seqs := map[float64]int{}
	shared := 0
	for _, item := range transcriptItems(full) {
		seq, _ := item["seq"].(float64)
		if seqs[seq]++; seqs[seq] == 2 {
			shared++
		}
	}
	if len(want) < 8 || shared < 2 {
		t.Fatalf("the chat needs several items on shared seqs: %s", full.body)
	}

	c := newWebClient(t, s.base, nil)
	c.http = oidcBrowser(t, s.base, s.idp, "owner")
	c.session("oidc")
	surfaces := []struct {
		path string
		get  func(string) apiReply
	}{
		{"/api/v1/threads/", func(path string) apiReply { return s.do(http.MethodGet, path, owner, nil) }},
		{"/web/v1/threads/", func(path string) apiReply { return c.do(http.MethodGet, path, nil) }},
	}
	for _, surface := range surfaces {
		for _, limit := range []int{1, 2, 3} {
			var got []string
			cursor := ""
			for range len(want) + 1 {
				query := url.Values{"limit": {strconv.Itoa(limit)}}
				if cursor != "" {
					query.Set("cursor", cursor)
				}
				page := surface.get(surface.path + strconv.FormatInt(id, 10) + "?" + query.Encode())
				if page.status != http.StatusOK {
					t.Fatalf("%s limit %d: %d %s", surface.path, limit, page.status, page.body)
				}
				got = append(encodedItems(t, transcriptItems(page)), got...)
				next, _ := page.json["next_cursor"].(string)
				if next == "" {
					break
				}
				cursor = next
			}
			if len(got) != len(want) {
				t.Fatalf("%s limit %d: %d items paged, %d unpaged:\npaged %v\nunpaged %v", surface.path, limit, len(got), len(want), got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s limit %d: item %d differs:\npaged %s\nunpaged %s", surface.path, limit, i, got[i], want[i])
				}
			}
		}
	}
}

func encodedItems(t *testing.T, items []map[string]any) []string {
	t.Helper()
	out := make([]string, len(items))
	for i, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = string(raw)
	}
	return out
}
