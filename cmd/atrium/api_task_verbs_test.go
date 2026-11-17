package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestTaskVerbsLinksFiltersAndUndo(t *testing.T) {
	t.Parallel()
	s := newStreamSite(t)
	frames := appFrames(t, s.openApp())
	synced := syncedRevisions(t, nextAppFrame(t, frames, "sync"))
	exec := func(query string, args ...any) { asstExec(t, s.d, query, args...) }
	exec("INSERT INTO threads (id, kind, title, effort, created_at, updated_at) VALUES (700, 'chat', 'Lease renewal', 'low', 1, 1)")
	exec("INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')")
	exec("INSERT INTO mailboxes (account, name, imap_name, role, uidvalidity, highest_uid) VALUES ('icloud', 'INBOX', 'INBOX', 'inbox', 1, 1)")
	exec("INSERT INTO messages (account, mailbox_id, uidvalidity, uid, msgid, digest, subject, from_addr, first_seen) VALUES ('icloud', 1, 1, 1, 'lease@test', 'lease', 'Your lease offer', 'agent@example.com', 0)")
	made := s.do(http.MethodPost, "/api/v1/knowledge/spaces", s.token, map[string]any{"name": "Move house", "instructions": "Pack early.", "effort": "high", "context_limit": 200000, "private": false})
	if made.status != http.StatusCreated {
		t.Fatalf("create project: %d %s", made.status, made.body)
	}
	project := strconv.FormatInt(number(t, made.json["id"]), 10)
	record := strconv.FormatInt(number(t, apiData(t, s.do(http.MethodPost, "/api/v1/knowledge", s.token, map[string]any{"title": "Landlord", "body": "Pays on the first.", "scope": "personal"}), http.StatusCreated)["id"]), 10)

	call := func(method, path string, body any, status int) (map[string]any, int64) {
		t.Helper()
		got := s.do(method, path, s.token, body)
		data := apiData(t, got, status)
		effects, _ := got.json["effect_ids"].([]any)
		if len(effects) != 1 {
			t.Fatalf("%s %s: effect_ids %v", method, path, got.json["effect_ids"])
		}
		return data, number(t, effects[0])
	}
	undo := func(effect int64) {
		t.Helper()
		if got := s.do(http.MethodPost, fmt.Sprintf("/api/v1/undo/write:%d", effect), s.token, nil); got.status != http.StatusOK {
			t.Fatalf("undo %d: %d %s", effect, got.status, got.body)
		}
	}
	get := func(id string) map[string]any {
		t.Helper()
		return apiData(t, s.do(http.MethodGet, "/api/v1/tasks/"+id, s.token, nil), http.StatusOK)["task"].(map[string]any)
	}
	link := func(task map[string]any) map[string]any {
		t.Helper()
		links := task["links"].([]any)
		if len(links) != 1 {
			t.Fatalf("links = %v", task["links"])
		}
		return links[0].(map[string]any)
	}

	created, effect := call(http.MethodPost, "/api/v1/tasks", map[string]any{"title": "Sign the lease", "due": "2020-03-10", "link": map[string]any{"kind": "thread", "id": "700"}}, http.StatusCreated)
	waitAppChange(t, frames, "tasks", synced["tasks"])
	task := created["task"].(map[string]any)
	id := strconv.FormatInt(number(t, task["id"]), 10)
	if l := link(task); task["ref"] != "task:"+id || l["kind"] != "thread" || l["id"] != "700" || l["title"] != "Lease renewal" {
		t.Fatalf("created task = %v", task)
	}
	undo(effect)
	expectAPI(t, s.do(http.MethodGet, "/api/v1/tasks/"+id, s.token, nil), http.StatusNotFound, "not_found")

	created, _ = call(http.MethodPost, "/api/v1/tasks", map[string]any{"title": "Sign the lease", "notes": "Bring a pen.", "due": "2020-03-10", "link": map[string]any{"kind": "message", "id": "1"}}, http.StatusCreated)
	id = strconv.FormatInt(number(t, created["task"].(map[string]any)["id"]), 10)
	path := "/api/v1/tasks/" + id
	if l := link(created["task"].(map[string]any)); l["title"] != "Your lease offer" {
		t.Fatalf("message link = %v", l)
	}
	for _, l := range [][3]string{{"project", project, "Move house"}, {"record", record, "Landlord"}, {"person", "pat@example.com", "pat@example.com"}} {
		k, v, want := l[0], l[1], l[2]
		edited, _ := call(http.MethodPatch, path, map[string]any{"link": map[string]any{"kind": k, "id": v}}, http.StatusOK)
		if l := link(edited["task"].(map[string]any)); l["kind"] != k || l["title"] != want {
			t.Fatalf("%s link = %v", k, l)
		}
	}
	for _, bad := range []map[string]any{{"kind": "thread", "id": "999"}, {"kind": "proposal", "id": "1"}, {"kind": "person", "id": "Pat <pat@example.com>"}} {
		expectField(t, s.do(http.MethodPatch, path, s.token, map[string]any{"link": bad}), "link")
	}
	expectField(t, s.do(http.MethodPatch, path, s.token, map[string]any{"clear": []string{"title"}}), "clear")

	before := get(id)
	edited, effect := call(http.MethodPatch, path, map[string]any{"title": "Sign and return the lease", "clear": []string{"notes", "due", "link"}, "updated_ts": before["updated_ts"]}, http.StatusOK)
	after := edited["task"].(map[string]any)
	if after["title"] != "Sign and return the lease" || after["notes"] != "" || after["due"] != nil || after["source_kind"] != "" || len(after["links"].([]any)) != 0 {
		t.Fatalf("edited task = %v", after)
	}
	conflict := s.do(http.MethodPatch, path, s.token, map[string]any{"title": "Stale", "updated_ts": before["updated_ts"]})
	expectAPI(t, conflict, http.StatusConflict, "stale")
	undo(effect)
	if restored := get(id); restored["title"] != "Sign the lease" || restored["notes"] != "Bring a pen." || restored["due"] != "2020-03-10" || link(restored)["kind"] != "person" {
		t.Fatalf("undone edit = %v", restored)
	}

	for _, step := range []struct{ prep, verb, from, to string }{{"", "done", "open", "done"}, {"done", "reopen", "done", "open"}, {"reopen", "dismiss", "open", "dismissed"}} {
		if step.prep != "" {
			call(http.MethodPost, path+"/"+step.prep, nil, http.StatusOK)
		}
		changed, effect := call(http.MethodPost, path+"/"+step.verb, nil, http.StatusOK)
		if changed["status"] != step.to || changed["task"].(map[string]any)["status"] != step.to {
			t.Fatalf("%s: %v", step.verb, changed)
		}
		expectAPI(t, s.do(http.MethodPost, path+"/"+step.verb, s.token, nil), http.StatusConflict, "conflict")
		undo(effect)
		if got := get(id); got["status"] != step.from {
			t.Fatalf("undo %s left %v", step.verb, got["status"])
		}
	}
	call(http.MethodPost, path+"/dismiss", nil, http.StatusOK)
	expectAPI(t, s.do(http.MethodPost, path+"/done", s.token, nil), http.StatusConflict, "conflict")
	if got := get(id); got["status"] != "dismissed" {
		t.Fatalf("done on a dismissed task left %v", got["status"])
	}
	expectAPI(t, s.do(http.MethodPost, path+"/reopen", s.token, nil), http.StatusOK, "")

	deleted, effect := call(http.MethodDelete, path, nil, http.StatusOK)
	if deleted["task"].(map[string]any)["title"] != "Sign the lease" {
		t.Fatalf("deleted = %v", deleted)
	}
	expectAPI(t, s.do(http.MethodGet, path, s.token, nil), http.StatusNotFound, "not_found")
	expectAPI(t, s.do(http.MethodDelete, path, s.token, nil), http.StatusNotFound, "not_found")
	undo(effect)
	if back := get(id); back["title"] != "Sign the lease" || link(back)["id"] != "pat@example.com" {
		t.Fatalf("undone delete = %v", back)
	}

	s.token = localAPIToken(t, "task-lists")
	today := time.Now().UTC()
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format(time.DateOnly) }
	for _, spec := range []map[string]any{
		{"title": "Book movers", "due": day(5), "link": map[string]any{"kind": "project", "id": project}},
		{"title": "Pay deposit", "due": day(-1)},
		{"title": "Change address", "notes": "post office"},
		{"title": "Return keys", "due": day(1) + "T09:00"},
		{"title": "Clean oven", "due": day(2)},
	} {
		call(http.MethodPost, "/api/v1/tasks", spec, http.StatusCreated)
	}
	cleaned := apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/tasks?q=oven", s.token, nil), http.StatusOK))[0].(map[string]any)
	call(http.MethodPost, fmt.Sprintf("/api/v1/tasks/%d/done", number(t, cleaned["id"])), nil, http.StatusOK)
	titles := func(query string) ([]string, map[string]any) {
		t.Helper()
		bearer := s.do(http.MethodGet, "/api/v1/tasks"+query, s.token, nil)
		data := apiData(t, bearer, http.StatusOK)
		var out []string
		for _, item := range apiItems(t, data) {
			out = append(out, item.(map[string]any)["title"].(string))
		}
		return out, data["counts"].(map[string]any)
	}
	order, counts := titles("")
	if fmt.Sprint(order) != "[Sign the lease Pay deposit Return keys Book movers Change address Clean oven]" {
		t.Fatalf("order = %v", order)
	}
	if counts["open"] != float64(5) || counts["done"] != float64(1) || counts["dismissed"] != float64(0) {
		t.Fatalf("counts = %v", counts)
	}
	for query, want := range map[string]string{
		"?status=done": "[Clean oven]",
		"?due_from=" + day(0) + "&due_to=" + day(5):          "[Return keys Book movers Clean oven]",
		"?status=open&due_to=" + day(1):                      "[Sign the lease Pay deposit Return keys]",
		"?link=" + url.QueryEscape("project:"+project):       "[Book movers]",
		"?link=" + url.QueryEscape("person:pat@example.com"): "[Sign the lease]",
		"?q=POST%20OFFICE":                                   "[Change address]",
	} {
		if got, _ := titles(query); fmt.Sprint(got) != want {
			t.Fatalf("tasks%s = %v, want %s", query, got, want)
		}
	}
	if _, counts := titles("?status=done&q=e"); counts["open"] != float64(5) || counts["done"] != float64(1) {
		t.Fatalf("counts ignore status: %v", counts)
	}
	expectField(t, s.do(http.MethodGet, "/api/v1/tasks?due_from=tomorrow", s.token, nil), "due_from")
	expectField(t, s.do(http.MethodGet, "/api/v1/tasks?link=project", s.token, nil), "link")

	first := apiData(t, s.do(http.MethodGet, "/api/v1/tasks?limit=4", s.token, nil), http.StatusOK)
	next, _ := first["next_cursor"].(string)
	rest := apiData(t, s.do(http.MethodGet, "/api/v1/tasks?limit=4&cursor="+url.QueryEscape(next), s.token, nil), http.StatusOK)
	var paged []string
	for _, item := range append(apiItems(t, first), apiItems(t, rest)...) {
		paged = append(paged, item.(map[string]any)["title"].(string))
	}
	if fmt.Sprint(paged) != fmt.Sprint(order) || rest["next_cursor"] != nil {
		t.Fatalf("paged = %v, next %v", paged, rest["next_cursor"])
	}

	browser := newWebClient(t, s.base, nil)
	browser.session("local")
	for _, query := range []string{"/tasks?limit=4", "/tasks/" + id} {
		bearer := s.do(http.MethodGet, "/api/v1"+query, s.token, nil)
		if cookie := browser.do(http.MethodGet, "/web/v1"+query, nil); bearer.status != http.StatusOK || cookie.body != bearer.body {
			t.Fatalf("%s differs between the surfaces: %.300s / %.300s", query, bearer.body, cookie.body)
		}
	}
	if got := apiData(t, browser.do(http.MethodPost, "/web/v1/tasks/"+id+"/dismiss", nil), http.StatusOK); got["status"] != "dismissed" {
		t.Fatalf("web dismiss: %v", got)
	}
}
