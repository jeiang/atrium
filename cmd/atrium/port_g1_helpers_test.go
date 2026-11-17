package main

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/db"
)

func localWebG1(t *testing.T, base string) *webClient {
	t.Helper()
	c := newWebClient(t, base, nil)
	c.session("local")
	return c
}

func webOKG1(t *testing.T, got apiReply) map[string]any {
	t.Helper()
	if got.status != http.StatusOK {
		t.Fatalf("status %d, want 200: %.600s", got.status, got.body)
	}
	return got.json
}

func webServeG1(t *testing.T) (string, *modelLog, *db.DB, *webClient) {
	t.Helper()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	return dir, api, d, localWebG1(t, startServeIn(t, dir))
}

func threadG1(id int64, rest string) string {
	return "/web/v1/threads/" + strconv.FormatInt(id, 10) + rest
}

func webStartG1(t *testing.T, c *webClient, fields map[string]any) int64 {
	t.Helper()
	made := c.do(http.MethodPost, "/web/v1/threads", fields)
	thread, _ := made.json["thread"].(map[string]any)
	if made.status != http.StatusCreated || thread == nil {
		t.Fatalf("POST /web/v1/threads = %d: %.600s", made.status, made.body)
	}
	return number(t, thread["id"])
}

func webFormG1(t *testing.T, c *webClient, method, path string, fields map[string]string, files []asstFile, headers ...string) apiReply {
	t.Helper()
	body, contentType := asstMultipart(t, fields, files)
	return c.do(method, path, body.String(), append([]string{"Content-Type", contentType}, headers...)...)
}

func webWantG1(t *testing.T, got apiReply, status int, contains ...string) {
	t.Helper()
	if got.status != status {
		t.Fatalf("status %d, want %d: %.600s", got.status, status, got.body)
	}
	for _, s := range contains {
		if !strings.Contains(got.body, s) {
			t.Errorf("response lacks %q: %.600s", s, got.body)
		}
	}
}

func webSearchG1(t *testing.T, api *modelLog, d *db.DB, c *webClient, query string) librarySearch {
	t.Helper()
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("lib", "library_search", map[string]any{"query": query})), asstReply("done"))
	thread := webStartG1(t, c, map[string]any{"text": "find " + query})
	if state := asstSettleThread(t, d, thread); state != "done" {
		t.Fatalf("library search run %s", state)
	}
	return lastToolResult(t, d, thread)
}
