package pages

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/threads"
)

var kitchen = url.Values{
	"name": {"Kitchen"}, "instructions": {"Always answer in metric units."}, "effort": {"high"}, "context_limit": {"200000"},
}

func with(base url.Values, kv ...string) url.Values {
	out := url.Values{}
	for k, v := range base {
		out[k] = append([]string(nil), v...)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out.Set(kv[i], kv[i+1])
	}
	return out
}

func (s *site) idAt(r *reply, prefix string) int64 {
	s.t.Helper()
	if r.StatusCode != http.StatusSeeOther {
		s.t.Fatalf("status %d, want 303: %s", r.StatusCode, r.body)
	}
	id, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("Location"), prefix), "/edit"), 10, 64)
	if err != nil {
		s.t.Fatalf("location %q: %v", r.Header.Get("Location"), err)
	}
	return id
}

func (s *site) newProject(kv ...string) int64 {
	s.t.Helper()
	return s.idAt(s.post("/projects", with(kitchen, kv...)), "/projects/")
}

func (s *site) newThread(title string, project int64) int64 {
	s.t.Helper()
	n := threads.Spec{Kind: threads.Chat, Title: &title}
	if project != 0 {
		n.ProjectID = &project
	}
	id, err := threads.Create(s.ctx(), s.db.Write, s.now(), n)
	if err != nil {
		s.t.Fatal(err)
	}
	return id
}

func (s *site) threadRow(id int64) (project *int64, effort string, limit int64) {
	s.t.Helper()
	if err := s.db.Read.QueryRow("SELECT project_id, effort, context_limit FROM threads WHERE id = ?", id).Scan(&project, &effort, &limit); err != nil {
		s.t.Fatal(err)
	}
	return project, effort, limit
}

func TestProjectsCreateEditAndArchive(t *testing.T) {
	s := newSite(t)
	status(t, s.get("/projects/1"), http.StatusNotFound)
	status(t, s.post("/projects/1", kitchen), http.StatusNotFound)
	if r := s.get("/projects"); r.StatusCode != http.StatusNotFound && r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /projects: %d, want 404 or 405", r.StatusCode)
	}

	newForm := status(t, s.get("/projects/new"), http.StatusOK)
	mustContain(t, newForm, "<html", "New project", `id="project-form"`, `action="/projects"`, `hx-target="#project-form"`, `<option value="90000" selected>`, `<option value="medium" selected>`)
	mustNotContain(t, newForm, "Create wiki hub", "/archive", `id="project-files"`, "project-dialog")

	blank := status(t, s.post("/projects", with(kitchen, "name", " ")), http.StatusUnprocessableEntity)
	mustContain(t, blank, "<html", "Give the project a name", `id="project-form"`, "Always answer in metric units.")
	if n := s.count("SELECT COUNT(*) FROM projects"); n != 0 {
		t.Fatalf("refused create stored %d projects", n)
	}

	bad := status(t, s.post("/projects", with(kitchen, "effort", "ultra"), hx, "true"), http.StatusUnprocessableEntity)
	mustContain(t, bad, "effort must be one of", `role="alert" tabindex="-1" autofocus`, `value="Kitchen"`, "Always answer in metric units.", `hx-post="/projects"`, `<option value="medium" selected>`)
	mustNotContain(t, bad, "<html")

	nameless := status(t, s.post("/projects", with(kitchen, "name", ""), hx, "true"), http.StatusUnprocessableEntity)
	mustContain(t, nameless, "Give the project a name", `role="alert" tabindex="-1" autofocus`, `<option value="high" selected>`)

	made := s.post("/projects", kitchen, hx, "true")
	if made.StatusCode != http.StatusNoContent || made.Header.Get("HX-Redirect") != "/projects/1/edit" || made.body != "" {
		t.Fatalf("htmx create: %d %v %q", made.StatusCode, made.Header, made.body)
	}
	plain := s.post("/projects", with(kitchen, "name", "Second", "instructions", ""))
	if plain.StatusCode != http.StatusSeeOther || plain.Header.Get("Location") != "/projects/2/edit" {
		t.Fatalf("create: %d %v", plain.StatusCode, plain.Header)
	}

	edit := status(t, s.get("/projects/1/edit"), http.StatusOK)
	mustContain(t, edit, "<html", "Always answer in metric units.", `value="Kitchen"`, `<option value="200000" selected>`, `<option value="high" selected>`, "Create wiki hub", `id="project-files"`, `action="/projects/1/files"`, `action="/projects/1/archive"`, `name="archived" value="1"`, `hx-confirm="Archive Kitchen?"`)
	mustNotContain(t, edit, "project-dialog", "Files every chat", "The assistant keeps this page")
	status(t, s.get("/projects/999/edit"), http.StatusNotFound)

	upd := s.post("/projects/1/edit", with(kitchen, "name", "Kitchen reno", "instructions", "Use cm."))
	if upd.StatusCode != http.StatusSeeOther || upd.Header.Get("Location") != "/projects/1/edit" {
		t.Fatalf("update: %d %v", upd.StatusCode, upd.Header)
	}
	mustContain(t, s.get("/projects/1/edit").body, "Kitchen reno", "Use cm.")
	hxUpdate := s.post("/projects/1/edit", with(kitchen, "name", "Kitchen reno"), hx, "true")
	if hxUpdate.StatusCode != http.StatusNoContent || hxUpdate.Header.Get("HX-Redirect") != "/projects/1/edit" {
		t.Fatalf("htmx update: %d %v", hxUpdate.StatusCode, hxUpdate.Header)
	}
	refused := status(t, s.post("/projects/1/edit", with(kitchen, "context_limit", "7"), hx, "true"), http.StatusUnprocessableEntity)
	mustContain(t, refused, "context limit must be one of", `value="Kitchen"`, `hx-post="/projects/1/edit"`)
	mustNotContain(t, refused, "<html")
	full := status(t, s.post("/projects/1/edit", with(kitchen, "context_limit", "7")), http.StatusUnprocessableEntity)
	mustContain(t, full, "<html", "context limit must be one of", `id="project-files"`)
	status(t, s.post("/projects/999/edit", kitchen), http.StatusNotFound)
	status(t, s.multipart("/chat", map[string]string{"text": "", "project_id": "999"}, nil), http.StatusNotFound)

	mustContain(t, status(t, s.get("/projects/x/edit"), http.StatusUnprocessableEntity), `"int_parsing"`, `"project_id"`)
	mustContain(t, status(t, s.post("/projects/x/edit", kitchen), http.StatusUnprocessableEntity), `"int_parsing"`)
	mustContain(t, status(t, s.post("/projects/1/archive", url.Values{"archived": {"x"}}), http.StatusUnprocessableEntity), `"int_parsing"`, `"archived"`)
	both := status(t, s.post("/projects/x/archive", url.Values{"archived": {"x"}}), http.StatusUnprocessableEntity)
	mustContain(t, both, `"project_id"`, `"archived"`)
	status(t, s.post("/projects/999/archive", url.Values{"archived": {"1"}}), http.StatusNotFound)

	archived := s.post("/projects/1/archive", url.Values{"archived": {"1"}})
	if archived.StatusCode != http.StatusSeeOther || archived.Header.Get("Location") != "/chat" {
		t.Fatalf("archive: %d %v", archived.StatusCode, archived.Header)
	}
	gone := status(t, s.get("/projects/1/edit"), http.StatusOK)
	mustContain(t, gone, `data-variant="secondary">archived</span>`, "Restore", `name="archived" value="0"`)
	mustNotContain(t, gone, "Add files", "hx-confirm")
	restoreHX := s.post("/projects/1/archive", url.Values{"archived": {"0"}}, hx, "true")
	if restoreHX.StatusCode != http.StatusNoContent || restoreHX.Header.Get("HX-Redirect") != "/projects/1/edit" {
		t.Fatalf("htmx restore: %d %v", restoreHX.StatusCode, restoreHX.Header)
	}
	hxArchive := s.post("/projects/1/archive", nil, hx, "true")
	if hxArchive.StatusCode != http.StatusNoContent || hxArchive.Header.Get("HX-Redirect") != "/chat" {
		t.Fatalf("htmx archive: %d %v", hxArchive.StatusCode, hxArchive.Header)
	}
	back := s.post("/projects/1/archive", url.Values{"archived": {"0"}})
	if back.StatusCode != http.StatusSeeOther || back.Header.Get("Location") != "/projects/1/edit" {
		t.Fatalf("restore: %d %v", back.StatusCode, back.Header)
	}
}

func TestChatListNamesProjects(t *testing.T) {
	s := newSite(t)
	project := s.newProject()
	older := s.newThread("Older", project)
	s.newThread("Elsewhere", 0)
	listing := mainOf(status(t, s.get("/chat"), http.StatusOK))
	if n := strings.Count(listing, "Kitchen"); n != 1 {
		t.Errorf("chat list names the project %d times, want 1", n)
	}
	mustContain(t, listing, "Elsewhere", "Older")
	pinned := status(t, s.post(fmt.Sprintf("/chat/%d/pin", older), url.Values{"pinned": {"1"}}, hx, "true"), http.StatusOK)
	mustContain(t, pinned, "Older", "Elsewhere", `aria-label="Pinned"`)
	mustNotContain(t, pinned, "project_id")
}

func mainOf(page string) string {
	return page[strings.Index(page, `id="content"`):]
}

func TestThreadsMoveIntoAndOutOfAProjectAndTakeItsDefaults(t *testing.T) {
	s := newSite(t)
	project := s.newProject("effort", "xhigh", "context_limit", "500000")
	loose := s.newThread("Loose", 0)
	id := strconv.FormatInt(loose, 10)
	header := status(t, s.get("/chat/"+id), http.StatusOK)
	mustContain(t, header, `action="/chat/`+id+`/project"`, "Kitchen")
	moved := s.post("/chat/"+id+"/project", url.Values{"project_id": {strconv.FormatInt(project, 10)}})
	if moved.StatusCode != http.StatusSeeOther {
		t.Fatalf("move: %d", moved.StatusCode)
	}
	if got, _, _ := s.threadRow(loose); got == nil || *got != project {
		t.Errorf("thread project %v", got)
	}
	mustContain(t, s.get("/chat/"+id).body, fmt.Sprintf(`href="/projects/%d/edit"`, project))
	s.post("/chat/"+id+"/project", url.Values{"project_id": {"0"}})
	if got, _, _ := s.threadRow(loose); got != nil {
		t.Errorf("thread still in project %d", *got)
	}
	status(t, s.post("/chat/"+id+"/project", url.Values{"project_id": {"999"}}), http.StatusNotFound)
	status(t, s.post(fmt.Sprintf("/projects/%d/archive", project), url.Values{"archived": {"1"}}), http.StatusSeeOther)
	status(t, s.post("/chat/"+id+"/project", url.Values{"project_id": {strconv.FormatInt(project, 10)}}), http.StatusUnprocessableEntity)
	status(t, s.post(fmt.Sprintf("/projects/%d/archive", project), url.Values{"archived": {"0"}}), http.StatusSeeOther)

	created := s.multipart("/chat", map[string]string{"text": "", "project_id": strconv.FormatInt(project, 10)}, nil)
	in := s.idAt(created, "/chat/")
	if got, effort, limit := s.threadRow(in); got == nil || *got != project || effort != "xhigh" || limit != 500_000 {
		t.Errorf("project thread: %v %s %d", got, effort, limit)
	}
	plain := s.idAt(s.multipart("/chat", map[string]string{"text": ""}, nil), "/chat/")
	if got, effort, limit := s.threadRow(plain); got != nil || effort != "medium" || limit != 90_000 {
		t.Errorf("plain thread: %v %s %d", got, effort, limit)
	}
	direct := s.newThread("Direct", project)
	if _, effort, _ := s.threadRow(direct); effort != "xhigh" {
		t.Errorf("created thread effort %s", effort)
	}
}
func TestProjectFormRefusalsKeepValidSelectValues(t *testing.T) {
	s := newSite(t)
	id := s.newProject("effort", "xhigh", "context_limit", "500000")
	path := fmt.Sprintf("/projects/%d/edit", id)
	for _, test := range []struct {
		name, effort, limit, selected, selectedEffort string
	}{
		{name: "submitted context", effort: "high", limit: "200000", selected: `<option value="200000" selected>`, selectedEffort: `<option value="high" selected>`},
		{name: "invalid context", effort: "high", limit: "abc", selected: `<option value="500000" selected>`, selectedEffort: `<option value="high" selected>`},
		{name: "invalid effort", effort: "invalid", limit: "500000", selected: `<option value="500000" selected>`, selectedEffort: `<option value="xhigh" selected>`},
	} {
		body := status(t, s.post(path, with(kitchen, "name", "", "effort", test.effort, "context_limit", test.limit), hx, "true"), http.StatusUnprocessableEntity)
		start := strings.Index(body, `id="project-context"`)
		if start < 0 {
			t.Fatalf("%s: missing context select: %s", test.name, body)
		}
		end := strings.Index(body[start:], `</select>`)
		if end < 0 {
			t.Fatalf("%s: missing context select end: %s", test.name, body)
		}
		contextSelect := body[start : start+end]
		if count := strings.Count(contextSelect, " selected>"); count != 1 || !strings.Contains(contextSelect, test.selected) {
			t.Errorf("%s: context select has %d selected options, want only %q: %s", test.name, count, test.selected, contextSelect)
		}
		effortStart := strings.Index(body, `id="project-effort"`)
		if effortStart < 0 {
			t.Fatalf("%s: missing effort select: %s", test.name, body)
		}
		effortEnd := strings.Index(body[effortStart:], `</select>`)
		if effortEnd < 0 {
			t.Fatalf("%s: missing effort select end: %s", test.name, body)
		}
		effortSelect := body[effortStart : effortStart+effortEnd]
		if count := strings.Count(effortSelect, " selected>"); count != 1 || !strings.Contains(effortSelect, test.selectedEffort) {
			t.Errorf("%s: effort select has %d selected options, want only %q: %s", test.name, count, test.selectedEffort, effortSelect)
		}
		mustContain(t, body, `role="alert" tabindex="-1" autofocus`)
	}
	newForm := status(t, s.post("/projects", with(kitchen, "name", "", "context_limit", "abc"), hx, "true"), http.StatusUnprocessableEntity)
	mustContain(t, newForm, `<option value="90000" selected>`)
}
