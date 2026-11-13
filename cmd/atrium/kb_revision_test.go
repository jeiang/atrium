package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestKnowledgeDetailProfileAndPrivacyRevisions(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)
	id := knowledgeCreate(t, w, url.Values{"title": {"Boiler"}, "body": {"Downstairs"}, "scope": {"personal"}})
	detail := fmt.Sprintf("/library/knowledge/%d", id)
	tags := make(map[string]string)
	for _, path := range []string{detail, "/library/knowledge/profile"} {
		code, body, header := w.get(path, false)
		tags[path] = header.Get("ETag")
		if code != http.StatusOK || tags[path] == "" {
			t.Fatalf("GET %s = %d: %.300s", path, code, body)
		}
		if code, body, _ := w.get(path, false, "If-None-Match", tags[path]); code != http.StatusNotModified || body != "" {
			t.Fatalf("unchanged %s = %d: %.300s", path, code, body)
		}
	}
	if code, body, _ := w.get("/library/knowledge/999999", false, "If-None-Match", tags[detail]); code != http.StatusNotFound {
		t.Fatalf("missing detail with validator = %d: %.300s", code, body)
	}
	if code, body, _ := w.post(detail, url.Values{"title": {"Boiler"}, "body": {"Upstairs"}, "scope": {"personal"}, "pinned": {"1"}}, false); code != http.StatusSeeOther {
		t.Fatalf("edit detail = %d: %.300s", code, body)
	}
	for path, tag := range tags {
		if code, body, header := w.get(path, false, "If-None-Match", tag); code != http.StatusOK || header.Get("ETag") == tag {
			t.Fatalf("changed %s = %d: %.300s", path, code, body)
		}
	}
	form := url.Values{"name": {"Household"}, "effort": {"medium"}, "context_limit": {"90000"}}
	code, body, header := w.post("/projects", form, false)
	if code != http.StatusSeeOther {
		t.Fatalf("create project = %d: %.300s", code, body)
	}
	edit := header.Get("Location")
	if !strings.HasPrefix(edit, "/projects/") {
		t.Fatalf("project redirect = %q", edit)
	}
	_, _, before := w.get(detail, false)
	form.Set("private", "1")
	if code, body, _ := w.post(edit, form, false); code != http.StatusSeeOther {
		t.Fatalf("change project privacy = %d: %.300s", code, body)
	}
	if code, body, after := w.get(detail, false, "If-None-Match", before.Get("ETag")); code != http.StatusOK || after.Get("ETag") == before.Get("ETag") {
		t.Fatalf("detail after privacy change = %d: %.300s", code, body)
	}
}
