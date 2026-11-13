package main

import (
	"fmt"
	"net/http"
	"testing"
)

func taskTitles(t *testing.T, s *apiSite, token, status string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, item := range apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/tasks?limit=100&status="+status, token, nil), http.StatusOK)) {
		task := item.(map[string]any)
		out[task["title"].(string)] = task
	}
	return out
}

func TestTasksAPI(t *testing.T) {
	s, token, d := localAPISite(t)
	added := apiData(t, s.do(http.MethodPost, "/api/v1/tasks", token, map[string]any{"title": "Call the plumber", "due": "2026-12-01"}), http.StatusCreated)
	id := number(t, added["task_id"])
	task := fmt.Sprintf("/api/v1/tasks/%d", id)
	tag := expectConditional(t, s, token, "/api/v1/tasks?status=open")

	apiData(t, s.do(http.MethodPatch, task, token, map[string]any{"title": "Call the plumber again"}), http.StatusOK)
	open := expectChanged(t, s, token, "/api/v1/tasks?status=open", tag)
	items := apiItems(t, open)
	if len(items) != 1 || items[0].(map[string]any)["title"] != "Call the plumber again" || items[0].(map[string]any)["due"] != "2026-12-01" {
		t.Fatalf("after title update: %v", items)
	}

	done := s.do(http.MethodPost, task+"/done", token, nil)
	effects, _ := done.json["effect_ids"].([]any)
	if data := apiData(t, done, http.StatusOK); data["status"] != "done" || len(effects) != 1 {
		t.Fatalf("done: %s", done.body)
	}
	if _, ok := taskTitles(t, s, token, "done")["Call the plumber again"]; !ok {
		t.Fatal("done task missing from the done filter")
	}
	if _, ok := taskTitles(t, s, token, "open")["Call the plumber again"]; ok {
		t.Fatal("done task still in the open filter")
	}
	if undo := s.do(http.MethodPost, fmt.Sprintf("/api/v1/undo/write:%d", number(t, effects[0])), token, nil); undo.status != http.StatusOK {
		t.Fatalf("undo: %d %s", undo.status, undo.body)
	}
	if got, ok := taskTitles(t, s, token, "open")["Call the plumber again"]; !ok || got["status"] != "open" {
		t.Fatalf("undo left %v", got)
	}

	expectField(t, s.do(http.MethodPost, "/api/v1/tasks", token, map[string]any{"title": ""}), "title")
	expectField(t, s.do(http.MethodGet, "/api/v1/tasks?status=bogus", token, nil), "status")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/tasks/999999/done", token, nil), http.StatusNotFound, "not_found")

	for i := range 104 {
		if _, err := d.Write.Exec("INSERT INTO tasks (title, created_ts, updated_ts) VALUES (?, ?, ?)", fmt.Sprintf("Chore %03d", i), 1000+i, 1000+i); err != nil {
			t.Fatal(err)
		}
	}
	first, rest, cursor := pageAll(t, s, token, "/api/v1/tasks?status=open&limit=100")
	seen := map[any]bool{}
	for _, item := range append(first, rest...) {
		seen[item.(map[string]any)["id"]] = true
	}
	if len(rest) != 5 || len(seen) != 105 {
		t.Fatalf("pages: %d then %d items, %d distinct", len(first), len(rest), len(seen))
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/tasks?status=done&cursor="+cursor, token, nil), http.StatusBadRequest, "bad_cursor")
}
