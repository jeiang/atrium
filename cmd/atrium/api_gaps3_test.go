package main

import (
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"
)

func TestDefaultsAndPermissionsHonorIfMatch(t *testing.T) {
	t.Parallel()
	s, token, _ := localAPISite(t)
	browser := newWebClient(t, s.base, nil)
	browser.session("local")

	cfg := s.do(http.MethodGet, "/api/v1/assistant/configuration", token, nil)
	efforts, _ := cfg.json["efforts"].([]any)
	current, _ := cfg.json["defaults"].(map[string]any)
	if cfg.status != http.StatusOK || len(efforts) < 2 || current == nil {
		t.Fatalf("configuration: %d %s", cfg.status, cfg.body)
	}
	limit := number(t, current["context_limit"])
	other := efforts[0]
	if other == current["effort"] {
		other = efforts[1]
	}
	read := cfg.header.Get("ETag")
	if read == "" {
		t.Fatalf("configuration has no ETag: %v", cfg.header)
	}
	if got := s.do(http.MethodPut, "/api/v1/assistant/defaults", token, map[string]any{"effort": other, "context_limit": limit}, "If-Match", read); got.status != http.StatusOK || got.json["effort"] != other {
		t.Fatalf("defaults with a current If-Match: %d %s", got.status, got.body)
	}
	expectAPI(t, s.do(http.MethodPut, "/api/v1/assistant/defaults", token, map[string]any{"effort": current["effort"], "context_limit": limit}, "If-Match", read), http.StatusPreconditionFailed, "precondition_failed")
	expectAPI(t, browser.do(http.MethodPut, "/web/v1/assistant/defaults", map[string]any{"effort": current["effort"], "context_limit": limit}, "If-Match", read), http.StatusPreconditionFailed, "precondition_failed")
	if stored, _ := s.do(http.MethodGet, "/api/v1/assistant/configuration", token, nil).json["defaults"].(map[string]any); stored["effort"] != other {
		t.Fatalf("a refused defaults change was applied: %v", stored)
	}
	fresh := browser.do(http.MethodGet, "/web/v1/assistant/configuration", nil).header.Get("ETag")
	if got := browser.do(http.MethodPut, "/web/v1/assistant/defaults", map[string]any{"effort": current["effort"], "context_limit": limit}, "If-Match", fresh); got.status != http.StatusOK {
		t.Fatalf("web defaults with a current If-Match: %d %s", got.status, got.body)
	}
	if got := s.do(http.MethodPut, "/api/v1/assistant/defaults", token, map[string]any{"effort": other, "context_limit": limit}); got.status != http.StatusOK {
		t.Fatalf("defaults without If-Match: %d %s", got.status, got.body)
	}

	const key = "permission_internet_clean"
	effective := func() string {
		t.Helper()
		got := apiData(t, s.do(http.MethodGet, "/api/v1/permissions", token, nil), http.StatusOK)
		for _, r := range got["rules"].([]any) {
			if rule := r.(map[string]any); rule["key"] == key {
				return rule["effective"].(string)
			}
		}
		t.Fatalf("no %s rule: %v", key, got)
		return ""
	}
	change := func(value string) map[string]any {
		return map[string]any{"changes": []any{map[string]any{"key": key, "value": value}}}
	}
	tag := s.do(http.MethodGet, "/api/v1/permissions", token, nil).header.Get("ETag")
	if tag == "" {
		t.Fatal("permissions have no ETag")
	}
	apiData(t, s.do(http.MethodPut, "/api/v1/permissions", token, change("ask"), "If-Match", tag), http.StatusOK)
	expectAPI(t, s.do(http.MethodPut, "/api/v1/permissions", token, change("deny"), "If-Match", tag), http.StatusPreconditionFailed, "precondition_failed")
	expectAPI(t, browser.do(http.MethodPut, "/web/v1/permissions", change("deny"), "If-Match", tag), http.StatusPreconditionFailed, "precondition_failed")
	if got := effective(); got != "ask" {
		t.Fatalf("a refused permissions change was applied: %s", got)
	}
	webTag := browser.do(http.MethodGet, "/web/v1/permissions", nil).header.Get("ETag")
	if got := browser.do(http.MethodPut, "/web/v1/permissions", change("deny"), "If-Match", webTag); got.status != http.StatusOK {
		t.Fatalf("web permissions with a current If-Match: %d %s", got.status, got.body)
	}
	apiData(t, s.do(http.MethodPut, "/api/v1/permissions", token, change("allow")), http.StatusOK)
	if got := effective(); got != "allow" {
		t.Fatalf("permissions without If-Match: %s", got)
	}
}

func TestSchedulePreviewAndToolCatalog(t *testing.T) {
	t.Parallel()
	s, token, _ := localAPISite(t)
	browser := newWebClient(t, s.base, nil)
	browser.session("local")

	preview := apiBody(t, s.do(http.MethodGet, "/api/v1/schedules/preview?schedule="+url.QueryEscape("daily 07:30"), token, nil), http.StatusOK)
	zone, err := time.LoadLocation(preview["tz"].(string))
	if err != nil {
		t.Fatalf("preview zone %v: %v", preview["tz"], err)
	}
	runs, _ := preview["runs"].([]any)
	if len(runs) != 5 || preview["schedule"] != "daily 07:30" {
		t.Fatalf("preview: %v", preview)
	}
	var last time.Time
	for _, raw := range runs {
		at, err := time.Parse(time.RFC3339, raw.(string))
		if err != nil {
			t.Fatalf("preview run %v: %v", raw, err)
		}
		local := at.In(zone)
		if local.Hour() != 7 || local.Minute() != 30 || !at.After(last) || !at.After(time.Now()) || raw.(string) != local.Format(time.RFC3339) {
			t.Fatalf("preview run %s is not the next 07:30 in %s after %s", raw, zone, last)
		}
		last = at
	}
	if web := browser.do(http.MethodGet, "/web/v1/schedules/preview?schedule="+url.QueryEscape("daily 07:30"), nil); web.status != http.StatusOK {
		t.Fatalf("web preview: %d %s", web.status, web.body)
	}
	bad := s.do(http.MethodGet, "/api/v1/schedules/preview?schedule=whenever", token, nil)
	expectAPI(t, bad, http.StatusUnprocessableEntity, "invalid")
	if fields, _ := bad.json["error"].(map[string]any)["fields"].([]any); len(fields) != 1 || !slices.Contains(fields[0].(map[string]any)["loc"].([]any), any("schedule")) {
		t.Fatalf("bad cadence error: %s", bad.body)
	}

	catalog := apiBody(t, s.do(http.MethodGet, "/api/v1/assistant/tools", token, nil), http.StatusOK)
	tools := map[string]map[string]any{}
	for _, raw := range catalog["items"].([]any) {
		entry := raw.(map[string]any)
		tools[entry["name"].(string)] = entry
	}
	kinds := func(name string) []string {
		var out []string
		for _, k := range tools[name]["kinds"].([]any) {
			out = append(out, k.(string))
		}
		return out
	}
	samples, finding, search := tools["ops_samples"], tools["ops_finding"], tools["search_threads"]
	if samples == nil || !slices.Equal(kinds("ops_samples"), []string{"worker"}) || samples["permission"] != "none" || samples["effect"] != "read" || samples["description"] == "" {
		t.Fatalf("worker-only read tool: %v", samples)
	}
	if finding == nil || !slices.Equal(kinds("ops_finding"), []string{"worker"}) || finding["permission"] != "own" || finding["effect"] != "write_undo" {
		t.Fatalf("worker-only write tool: %v", finding)
	}
	if search == nil || slices.Contains(kinds("search_threads"), "worker") || !slices.Contains(kinds("search_threads"), "chat") {
		t.Fatalf("chat tool: %v", search)
	}
	if web := browser.do(http.MethodGet, "/web/v1/assistant/tools", nil); web.status != http.StatusOK {
		t.Fatalf("web tools: %d %s", web.status, web.body)
	}
}
