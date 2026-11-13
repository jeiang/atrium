package main

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func localAPISite(t *testing.T) (*apiSite, string, *db.DB) {
	t.Helper()
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	t.Cleanup(func() { _ = d.Close() })
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return &apiSite{t: t, base: base}, token, d
}

func apiData(t *testing.T, got apiReply, status int) map[string]any {
	t.Helper()
	if got.status != status {
		t.Fatalf("status %d, want %d: %.600s", got.status, status, got.body)
	}
	data, _ := got.json["data"].(map[string]any)
	return data
}

func apiItems(t *testing.T, data map[string]any) []any {
	t.Helper()
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("no items: %v", data)
	}
	return items
}

func expectConditional(t *testing.T, s *apiSite, token, path string) string {
	t.Helper()
	first := s.do(http.MethodGet, path, token, nil)
	tag := first.header.Get("ETag")
	if first.status != http.StatusOK || tag == "" {
		t.Fatalf("GET %s: %d %v %.300s", path, first.status, first.header, first.body)
	}
	if again := s.do(http.MethodGet, path, token, nil, "If-None-Match", tag); again.status != http.StatusNotModified {
		t.Fatalf("conditional GET %s: %d", path, again.status)
	}
	return tag
}

func expectChanged(t *testing.T, s *apiSite, token, path, tag string) map[string]any {
	t.Helper()
	got := s.do(http.MethodGet, path, token, nil, "If-None-Match", tag)
	if got.header.Get("ETag") == tag {
		t.Fatalf("GET %s kept ETag %s after a change", path, tag)
	}
	return apiData(t, got, http.StatusOK)
}

func expectField(t *testing.T, got apiReply, field string) {
	t.Helper()
	expectAPI(t, got, http.StatusUnprocessableEntity, "invalid")
	e, _ := got.json["error"].(map[string]any)
	fields, _ := e["fields"].([]any)
	for _, f := range fields {
		loc, _ := f.(map[string]any)["loc"].([]any)
		if len(loc) > 0 && loc[len(loc)-1] == field {
			return
		}
	}
	t.Fatalf("no field %q in %s", field, got.body)
}

func pageAll(t *testing.T, s *apiSite, token, path string) (first []any, rest []any, cursor string) {
	t.Helper()
	data := apiData(t, s.do(http.MethodGet, path, token, nil), http.StatusOK)
	first = apiItems(t, data)
	next, _ := data["next_cursor"].(string)
	if len(first) != 100 || next == "" {
		t.Fatalf("first page %d items, cursor %q", len(first), next)
	}
	sep := "?"
	if u, _ := url.Parse(path); u.RawQuery != "" {
		sep = "&"
	}
	more := apiData(t, s.do(http.MethodGet, path+sep+"cursor="+url.QueryEscape(next), token, nil), http.StatusOK)
	return first, apiItems(t, more), next
}

func TestKnowledgeAPILifecycle(t *testing.T) {
	s, token, d := localAPISite(t)
	created := apiData(t, s.do(http.MethodPost, "/api/v1/knowledge", token, map[string]any{
		"title": "Boiler code", "body": "The boiler code is 4417.", "scope": "work", "marks": []string{"pii"},
	}), http.StatusCreated)
	id := number(t, created["id"])
	record := fmt.Sprintf("/api/v1/knowledge/%d", id)
	tag := expectConditional(t, s, token, record)

	edit := s.do(http.MethodPatch, record, token, map[string]any{"title": "Boiler code", "body": "The boiler code is 9001.", "scope": "work", "marks": []string{"pii"}})
	effects, _ := edit.json["effect_ids"].([]any)
	if apiData(t, edit, http.StatusOK)["id"] != created["id"] || len(effects) != 1 {
		t.Fatalf("edit: %s", edit.body)
	}
	detail := expectChanged(t, s, token, record, tag)
	if rec := detail["record"].(map[string]any); rec["body"] != "The boiler code is 9001." || rec["marks"].([]any)[0] != "pii" {
		t.Fatalf("edited detail: %v", rec)
	}
	if undo := s.do(http.MethodPost, fmt.Sprintf("/api/v1/undo/write:%d", number(t, effects[0])), token, nil); undo.status != http.StatusOK {
		t.Fatalf("undo: %d %s", undo.status, undo.body)
	}
	if rec := apiData(t, s.do(http.MethodGet, record, token, nil), http.StatusOK)["record"].(map[string]any); rec["body"] != "The boiler code is 4417." {
		t.Fatalf("undo left body %v", rec["body"])
	}

	for _, step := range []struct{ action, field string }{{"confirm", "confirmed"}, {"retract", "retracted"}} {
		changed := apiData(t, s.do(http.MethodPost, record+"/"+step.action, token, nil), http.StatusOK)
		if rec := changed["record"].(map[string]any); rec[step.field] != true {
			t.Fatalf("%s: %v", step.action, rec)
		}
		if rec := apiData(t, s.do(http.MethodGet, record, token, nil), http.StatusOK)["record"].(map[string]any); rec[step.field] != true {
			t.Fatalf("after %s: %v", step.action, rec)
		}
	}
	expectAPI(t, s.do(http.MethodPost, record+"/confirm", token, nil), http.StatusConflict, "refused")
	if rec := apiData(t, s.do(http.MethodPost, record+"/restore", token, nil), http.StatusOK)["record"].(map[string]any); rec["retracted"] != false {
		t.Fatalf("restore: %v", rec)
	}
	if purge := s.do(http.MethodDelete, record, token, nil); purge.status != http.StatusNoContent {
		t.Fatalf("purge: %d %s", purge.status, purge.body)
	}
	expectAPI(t, s.do(http.MethodGet, record, token, nil), http.StatusNotFound, "not_found")

	expectField(t, s.do(http.MethodGet, "/api/v1/knowledge?scope=bogus", token, nil), "scope")
	expectField(t, s.do(http.MethodPost, "/api/v1/knowledge", token, map[string]any{"title": "", "body": "x", "scope": "work"}), "title")
	expectField(t, s.do(http.MethodPost, "/api/v1/knowledge/confirm-matching", token, map[string]any{"state": "all"}), "state")
	expectField(t, s.do(http.MethodPost, "/api/v1/knowledge/confirm", token, map[string]any{"ids": make([]int64, 101)}), "ids")

	kept := apiData(t, s.do(http.MethodPost, "/api/v1/knowledge", token, map[string]any{
		"title": "Locker code", "body": "The locker code is 1234.", "scope": "personal", "aliases": []string{"locker"}, "private": true, "marks": []string{"messages", "pii"}, "pinned": true,
	}), http.StatusCreated)
	keptPath := fmt.Sprintf("/api/v1/knowledge/%d", number(t, kept["id"]))
	apiData(t, s.do(http.MethodPatch, keptPath, token, map[string]any{"title": "Gym locker code"}), http.StatusOK)
	if rec := apiData(t, s.do(http.MethodGet, keptPath, token, nil), http.StatusOK)["record"].(map[string]any); rec["title"] != "Gym locker code" || rec["body"] != "The locker code is 1234." ||
		rec["scope"] != "personal" || rec["private"] != true || rec["pinned"] != true || len(rec["marks"].([]any)) != 2 || len(rec["aliases"].([]any)) != 1 {
		t.Fatalf("a title-only PATCH changed omitted fields: %v", rec)
	}
	apiData(t, s.do(http.MethodPost, keptPath+"/confirm", token, nil), http.StatusOK)

	for i := range 104 {
		scope := "work"
		if i == 0 {
			scope = "personal"
		}
		if _, err := d.Write.Exec("INSERT INTO kb_records (title, body, scope, writer, created_at, updated_at) VALUES (?, 'body', ?, 'operator', ?, ?)",
			fmt.Sprintf("Note %03d", i), scope, 1000+i, 1000+i); err != nil {
			t.Fatal(err)
		}
	}
	list := expectConditional(t, s, token, "/api/v1/knowledge?state=unconfirmed")
	first, rest, cursor := pageAll(t, s, token, "/api/v1/knowledge?state=unconfirmed&limit=100")
	if len(rest) != 4 || first[0].(map[string]any)["title"] != "Note 103" || rest[3].(map[string]any)["title"] != "Note 000" {
		t.Fatalf("pages: %d then %d items", len(first), len(rest))
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/knowledge?state=pinned&cursor="+url.QueryEscape(cursor), token, nil), http.StatusBadRequest, "bad_cursor")

	matched := apiData(t, s.do(http.MethodPost, "/api/v1/knowledge/confirm-matching", token, map[string]any{"state": "unconfirmed", "scope": "work"}), http.StatusOK)
	if committed, refused := matched["committed"].([]any), matched["refused"].([]any); len(committed) != 103 || len(refused) != 0 {
		t.Fatalf("confirm-matching: %v", matched)
	}
	remaining := expectChanged(t, s, token, "/api/v1/knowledge?state=unconfirmed", list)
	if items := apiItems(t, remaining); len(items) != 1 || items[0].(map[string]any)["scope"] != "personal" {
		t.Fatalf("unconfirmed after confirm-matching: %v", items)
	}
}
