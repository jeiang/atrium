package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestKnowledgeFullPagesAreUnconditional(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)
	id := knowledgeCreate(t, w, url.Values{"title": {"Boiler"}, "body": {"Downstairs"}, "scope": {"personal"}})
	detail := fmt.Sprintf("/library/knowledge/%d", id)
	_, _, fragment := w.get("/library/knowledge", true)
	tag := fragment.Get("ETag")
	if tag == "" {
		t.Fatal("list fragment carries no validator")
	}
	for _, path := range []string{"/library/knowledge", detail, "/library/knowledge/profile"} {
		for _, validator := range []string{tag, "*"} {
			if code, body, header := w.get(path, false, "If-None-Match", validator); code != http.StatusOK || header.Get("ETag") != "" || !strings.Contains(body, `id="pending-count"`) {
				t.Fatalf("full page %s with If-None-Match %s = %d etag %q: %.300s", path, validator, code, header.Get("ETag"), body)
			}
		}
	}
	if code, body, _ := w.post(detail, url.Values{"title": {"Boiler"}, "body": {"Upstairs"}, "scope": {"personal"}, "pinned": {"1"}}, false); code != http.StatusSeeOther {
		t.Fatalf("edit detail = %d: %.300s", code, body)
	}
	if code, body, _ := w.get(detail, false, "If-None-Match", "*"); code != http.StatusOK || !strings.Contains(body, "Upstairs") {
		t.Fatalf("detail after an edit = %d: %.300s", code, body)
	}
}
