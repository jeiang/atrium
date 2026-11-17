package main

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

func webG4(t *testing.T, base string) *webClient {
	t.Helper()
	c := newWebClient(t, base, nil)
	c.session("local")
	return c
}

func webAsG4(t *testing.T, base string, browser *http.Client) *webClient {
	t.Helper()
	c := newWebClient(t, base, nil)
	if browser != nil {
		c.http.Jar = browser.Jar
	}
	got := c.do(http.MethodGet, "/web/v1/session", nil)
	token, _ := got.json["csrf_token"].(string)
	if got.status != http.StatusOK || token == "" {
		t.Fatalf("web session on %s: %d %s", base, got.status, got.body)
	}
	c.csrf = token
	return c
}

func startG4(c *webClient, text string) (int64, int64) {
	c.t.Helper()
	made := c.do(http.MethodPost, "/web/v1/threads", map[string]any{"text": text})
	if made.status != http.StatusCreated {
		c.t.Fatalf("create thread on %s: %d %s", c.base, made.status, made.body)
	}
	thread, _ := made.json["thread"].(map[string]any)
	var run int64
	if made.json["run_id"] != nil {
		run = number(c.t, made.json["run_id"])
	}
	return number(c.t, thread["id"]), run
}

func sendG4(c *webClient, thread int64, text string) apiReply {
	c.t.Helper()
	sent := c.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/messages", thread), map[string]any{"text": text})
	if sent.status != http.StatusAccepted {
		c.t.Fatalf("send to thread %d: %d %s", thread, sent.status, sent.body)
	}
	return sent
}

func approveG4(c *webClient, proposal int64) apiReply {
	c.t.Helper()
	return c.do(http.MethodPost, fmt.Sprintf("/web/v1/approvals/%d/approve", proposal), nil)
}

func connPutG4(c *webClient, id, endpoint string, credentials map[string]string) apiReply {
	c.t.Helper()
	body := map[string]any{}
	if endpoint != "" {
		body["url"] = endpoint
	}
	if len(credentials) > 0 {
		body["credentials"] = credentials
	}
	return c.do(http.MethodPut, "/web/v1/connections/"+id, body)
}

func connTestG4(c *webClient, id string) apiReply {
	c.t.Helper()
	return c.do(http.MethodPost, "/web/v1/connections/"+id+"/test", nil)
}

func dataG4(r apiReply) map[string]any {
	data, _ := r.json["data"].(map[string]any)
	return data
}

func queueG4(c *webClient) apiReply {
	c.t.Helper()
	got := c.do(http.MethodGet, "/web/v1/media/queue", nil)
	if got.status != http.StatusOK {
		c.t.Fatalf("GET /web/v1/media/queue = %d: %.600s", got.status, got.body)
	}
	return got
}

func fileG4(c *webClient, id, kind, title, year string, episodes ...[2]string) apiReply {
	c.t.Helper()
	return refileG4(c, id, filingG4(c, id, kind, title, year, episodes...))
}

func refileG4(c *webClient, id string, body map[string]any) apiReply {
	c.t.Helper()
	return c.do(http.MethodPost, "/web/v1/media/filings/"+url.PathEscape(id), body)
}

func filingG4(c *webClient, id, kind, title, year string, episodes ...[2]string) map[string]any {
	c.t.Helper()
	if kind == "tv" {
		kind = "show"
	}
	body := map[string]any{"action": "file", "kind": kind, "title": title, "year": year}
	if len(episodes) > 0 {
		preview := c.do(http.MethodGet, "/web/v1/media/filings/"+url.PathEscape(id)+"?"+url.Values{"kind": {kind}, "title": {title}, "year": {year}}.Encode(), nil)
		files, _ := preview.json["files"].([]any)
		if preview.status != http.StatusOK || len(files) < len(episodes) {
			c.t.Fatalf("filing preview %s = %d: %.600s", id, preview.status, preview.body)
		}
		var list []map[string]any
		for i, ep := range episodes {
			file, _ := files[i].(map[string]any)
			list = append(list, map[string]any{"file": file["id"], "season": ep[0], "episode": ep[1]})
		}
		body["episodes"] = list
	}
	return body
}
