package main

import (
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

func webG3(t *testing.T, base string) *webClient {
	t.Helper()
	c := newWebClient(t, base, nil)
	c.session("local")
	return c
}

func okG3(t *testing.T, label string, got apiReply) map[string]any {
	t.Helper()
	if got.status < 200 || got.status > 299 {
		t.Fatalf("%s: %d %.600s", label, got.status, got.body)
	}
	return got.json
}

func dataG3(t *testing.T, label string, got apiReply) map[string]any {
	t.Helper()
	body := okG3(t, label, got)
	if data, ok := body["data"].(map[string]any); ok {
		return data
	}
	return body
}

func listG3(t *testing.T, label string, got apiReply, field string) []map[string]any {
	t.Helper()
	data, _ := okG3(t, label, got)["data"].(map[string]any)
	raw, _ := data[field].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, _ := item.(map[string]any)
		out = append(out, m)
	}
	return out
}

func activityG3(t *testing.T, c *webClient, query, source string, id int64) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, item := range listG3(t, "activity", c.do(http.MethodGet, "/web/v1/activity"+query, nil), "items") {
		if item["source"] == source && item["id"] == float64(id) {
			out = append(out, item)
		}
	}
	return out
}

func connTestG3(t *testing.T, s *connectionSite, id, url string, credentials map[string]string) apiReply {
	t.Helper()
	body := map[string]any{}
	if url != "" {
		body["url"] = url
	}
	if len(credentials) > 0 {
		body["credentials"] = credentials
	}
	return webG3(t, s.base).do(http.MethodPut, "/web/v1/connections/"+id, body)
}

func connDisconnectG3(t *testing.T, s *connectionSite, id string) {
	t.Helper()
	okG3(t, "disconnect "+id, webG3(t, s.base).do(http.MethodDelete, "/web/v1/connections/"+id, nil))
}

func connViewG3(t *testing.T, base, id string) map[string]any {
	t.Helper()
	for _, v := range listG3(t, "connections", webG3(t, base).do(http.MethodGet, "/web/v1/connections", nil), "connections") {
		if v["id"] == id {
			return v
		}
	}
	t.Fatalf("no connection %s on /web/v1/connections", id)
	return nil
}

var needClientsG3 sync.Map

func needStateG3(t *testing.T, base, module, connection string) string {
	t.Helper()
	key := struct {
		t    *testing.T
		base string
	}{t, base}
	client, ok := needClientsG3.Load(key)
	if !ok {
		client, _ = needClientsG3.LoadOrStore(key, webG3(t, base))
	}
	got := client.(*webClient).do(http.MethodGet, "/web/v1/connections/dependencies", nil)
	for _, need := range listG3(t, "dependencies", got, "needs") {
		if need["module"] != module {
			continue
		}
		candidates, _ := need["candidates"].([]any)
		if need["kind"] == connection || need["connection"] == connection || slices.Contains(candidates, any(connection)) {
			state, _ := need["state"].(string)
			return state
		}
	}
	t.Fatalf("no %s need on %s: %.4000s", module, connection, got.body)
	return ""
}

func hasG3(body string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(body, p) {
			return false
		}
	}
	return true
}
