package pages

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type jobRow struct {
	id                  int64
	name, prompt, sched string
	enabled, cancelled  bool
}

func (s *site) job(name string) jobRow {
	s.t.Helper()
	var j jobRow
	if err := s.db.Read.QueryRow("SELECT id, name, prompt, schedule, enabled, cancelled FROM jobs WHERE name = ?", name).
		Scan(&j.id, &j.name, &j.prompt, &j.sched, &j.enabled, &j.cancelled); err != nil {
		s.t.Fatalf("job %q: %v", name, err)
	}
	return j
}

func schedule(name, prompt, when string) url.Values {
	return url.Values{"name": {name}, "prompt": {prompt}, "schedule": {when}}
}

func wantListHeaders(t *testing.T, r *reply, trigger string) {
	t.Helper()
	if r.Header.Get("HX-Retarget") != "#scheduled-list" || r.Header.Get("HX-Reswap") != "outerHTML" || r.Header.Get("HX-Trigger") != trigger {
		t.Errorf("headers retarget=%q reswap=%q trigger=%q, want trigger %q", r.Header.Get("HX-Retarget"), r.Header.Get("HX-Reswap"), r.Header.Get("HX-Trigger"), trigger)
	}
}

func TestScheduledCreateEditToggleCancel(t *testing.T) {
	s := newSite(t)
	page := status(t, s.get("/scheduled"), http.StatusOK)
	mustContain(t, page, "nightly", "Every day at 02:30", "Fri 29 May 02:30", "weekly-review", "Sun 31 May 18:00", "No runs yet.", "Times are America/Port_of_Spain",
		`id="schedule-dialog"`, `hx-get="/scheduled/new"`, `id="scheduled-list"`, "Never")
	if n := s.count("SELECT COUNT(*) FROM jobs WHERE kind = 'nightly'"); n != 1 {
		t.Errorf("nightly jobs %d", n)
	}
	seeded := s.count("SELECT COUNT(*) FROM jobs")
	status(t, s.get("/scheduled"), http.StatusOK)
	if n := s.count("SELECT COUNT(*) FROM jobs"); n != seeded {
		t.Errorf("a second page load seeded jobs: %d -> %d", seeded, n)
	}
	partial := status(t, s.get("/scheduled", hx, "true"), http.StatusOK)
	mustContain(t, partial, `id="scheduled-list"`, "Every day at 02:30")
	mustNotContain(t, partial, "<html", `id="schedule-dialog"`)
	form := status(t, s.get("/scheduled/new"), http.StatusOK)
	mustContain(t, form, `name="schedule"`, "New schedule", `placeholder="daily 08:00"`, "Create")
	mustNotContain(t, form, "<html", "Recent runs", "Delete")

	created := s.post("/scheduled", schedule("Digest", "Summarize mail.", "weekly mon 08:00"), hx, "true")
	body := status(t, created, http.StatusOK)
	mustContain(t, body, "Digest", "Every Monday at 08:00", "Schedule created")
	mustNotContain(t, body, "<html")
	wantListHeaders(t, created, "close-dialogs")

	bad := s.post("/scheduled", schedule("Bad", "p", "nonsense"), hx, "true")
	mustContain(t, status(t, bad, http.StatusUnprocessableEntity), "unrecognised schedule", `value="Bad"`, `role="alert"`, "New schedule")
	if bad.Header.Get("HX-Retarget") != "" || bad.Header.Get("HX-Trigger") != "" {
		t.Errorf("a refused form carries list headers: %v", bad.Header)
	}
	mustContain(t, status(t, s.post("/scheduled", schedule(" ", "p", "daily 08:00"), hx, "true"), http.StatusUnprocessableEntity), "name and prompt are required")

	digest := s.job("Digest")
	id := fmt.Sprint(digest.id)
	edit := status(t, s.get("/scheduled/"+id+"/edit"), http.StatusOK)
	mustContain(t, edit, `value="Digest"`, "Recent runs", "Edit Digest", "No runs yet.", "Delete", `hx-post="/scheduled/`+id+`/cancel"`)
	mustNotContain(t, edit, "<html", "readonly")
	saved := s.post("/scheduled/"+id, schedule("Digest", "New prompt", "daily 09:00"), hx, "true")
	mustContain(t, status(t, saved, http.StatusOK), "Schedule saved", "Every day at 09:00")
	wantListHeaders(t, saved, "close-dialogs")
	if got := s.job("Digest"); got.prompt != "New prompt" || got.sched != "daily 09:00" {
		t.Errorf("saved job %+v", got)
	}
	refused := s.post("/scheduled/"+id, schedule("Renamed", "kept in form", "later"), hx, "true")
	mustContain(t, status(t, refused, http.StatusUnprocessableEntity), "Edit Digest", `value="Renamed"`, "kept in form", `value="later"`, `role="alert"`)
	if got := s.job("Digest"); got.prompt != "New prompt" {
		t.Errorf("a refused save changed the job: %+v", got)
	}

	toggled := s.post("/scheduled/"+id+"/toggle", nil, hx, "true")
	mustContain(t, status(t, toggled, http.StatusOK), "Schedule updated", "Paused")
	wantListHeaders(t, toggled, "close-dialogs")
	if s.job("Digest").enabled {
		t.Error("toggle did not disable the job")
	}
	s.post("/scheduled/"+id+"/toggle", nil, hx, "true")
	if !s.job("Digest").enabled {
		t.Error("second toggle did not enable the job")
	}
	s.exec("INSERT INTO jobs (name, prompt, schedule, tz, enabled, created_by, created_at) VALUES ('Past', 'p', '2020-01-01T09:00', 'UTC', 0, 'operator', 1)")
	past := s.post(fmt.Sprintf("/scheduled/%d/toggle", s.job("Past").id), nil, hx, "true")
	mustContain(t, status(t, past, http.StatusUnprocessableEntity), "Not saved", `id="scheduled-list"`)
	if past.Header.Get("HX-Retarget") != "#scheduled-list" || past.Header.Get("HX-Reswap") != "outerHTML" || past.Header.Get("HX-Trigger") != "" {
		t.Errorf("refused toggle headers %v", past.Header)
	}

	cancelled := s.post("/scheduled/"+id+"/cancel", nil, hx, "true")
	body = status(t, cancelled, http.StatusOK)
	mustContain(t, body, "Schedule deleted")
	mustNotContain(t, body, "Digest")
	wantListHeaders(t, cancelled, "close-dialogs")
	if !s.job("Digest").cancelled {
		t.Error("cancel did not cancel the job")
	}
	status(t, s.get("/scheduled/"+id+"/edit"), http.StatusNotFound)
	status(t, s.post("/scheduled/"+id+"/toggle", nil), http.StatusNotFound)
	status(t, s.post("/scheduled/999/cancel", nil), http.StatusNotFound)
	status(t, s.post("/scheduled/999", schedule("a", "b", "daily 08:00")), http.StatusNotFound)
	status(t, s.get("/scheduled/999/edit"), http.StatusNotFound)
	mustContain(t, status(t, s.get("/scheduled/x/edit"), http.StatusUnprocessableEntity), `"int_parsing"`, `"job_id"`)
	status(t, s.get("/settings/schedules"), http.StatusNotFound)

	builtin := s.job("weekly-review")
	refusedCancel := s.post(fmt.Sprintf("/scheduled/%d/cancel", builtin.id), nil, hx, "true")
	mustContain(t, status(t, refusedCancel, http.StatusUnprocessableEntity), "built-in runs cannot be cancelled; disable them instead", "Not saved")
	if refusedCancel.Header.Get("HX-Trigger") != "" {
		t.Errorf("refused cancel closes dialogs: %v", refusedCancel.Header)
	}
}

func TestScheduledFixedJobsOnlyTakeASchedule(t *testing.T) {
	s := newSite(t)
	status(t, s.get("/scheduled"), http.StatusOK)
	nightly := s.job("nightly")
	id := fmt.Sprint(nightly.id)
	form := status(t, s.get("/scheduled/"+id+"/edit"), http.StatusOK)
	mustContain(t, form, "The nightly prompt is fixed in code.", `rows="5"`, "readonly")
	mustNotContain(t, form, "Delete", `hx-post="/scheduled/`+id+`/cancel"`)
	saved := s.post("/scheduled/"+id, schedule("hacked", "hacked", "daily 03:00"), hx, "true")
	status(t, saved, http.StatusOK)
	got := s.job("nightly")
	if got.sched != "daily 03:00" || got.name != "nightly" || got.prompt != nightly.prompt {
		t.Errorf("fixed job after save: %+v", got)
	}
}

func TestScheduledShowsRunsInThePageZone(t *testing.T) {
	s := newSite(t)
	status(t, s.get("/scheduled"), http.StatusOK)
	nightly := s.job("nightly")
	thread := s.newThread("morning digest thread", 0)
	res, err := s.db.Write.Exec("INSERT INTO job_runs (job_id, thread_id, due_at, started_at, ended_at, status, tokens, cost) VALUES (?, ?, 1779999000, 1780000000, 1780000100, 'ok', 10, 0.123)", nightly.id, thread)
	if err != nil {
		t.Fatal(err)
	}
	last, _ := res.LastInsertId()
	s.exec("INSERT INTO job_runs (job_id, due_at, status, error) VALUES (?, 1780003600, 'error', 'Budget <exceeded>')", nightly.id)
	s.exec("UPDATE jobs SET last_run_id = ? WHERE id = ?", last, nightly.id)
	s.exec("INSERT INTO jobs (name, prompt, schedule, tz, next_due, created_by, created_at) VALUES ('Once', 'p', '2030-01-01T10:00', 'UTC', 1893492000, 'agent', 1)")

	page := status(t, s.get("/scheduled"), http.StatusOK)
	mustContain(t, page, "Thu 28 May 16:26", "Thu 28 May 17:26", "$0.123", fmt.Sprintf(`href="/chat/%d"`, thread), "Budget &lt;exceeded&gt;",
		`data-variant="destructive" title="Budget &lt;exceeded&gt;">error`, "Once on Tue 01 Jan 2030 at 10:00", "Tue 01 Jan 06:00", "agent")
	mustNotContain(t, page, "Budget <exceeded>")

	edit := status(t, s.get(fmt.Sprintf("/scheduled/%d/edit", nightly.id)), http.StatusOK)
	mustContain(t, edit, "Recent runs", "Thu 28 May 16:26", "$0.123")
	mustNotContain(t, edit, `<span class="font-medium">nightly</span>`)
	wide := status(t, s.get("/scheduled", hx, "true"), http.StatusOK)
	mustContain(t, wide, `<span class="font-medium">nightly</span>`)
}

func TestScheduledWithoutScriptAnswersWholePages(t *testing.T) {
	s := newSite(t)
	made := s.post("/scheduled", schedule("Plain", "p", "daily 08:00"))
	if made.StatusCode != http.StatusSeeOther || made.Header.Get("Location") != "/scheduled" {
		t.Fatalf("create: %d %v", made.StatusCode, made.Header)
	}
	id := fmt.Sprint(s.job("Plain").id)
	mustContain(t, status(t, s.post("/scheduled/"+id, schedule("Plain", "p", "nonsense")), http.StatusUnprocessableEntity), "Not saved")
	for _, path := range []string{"/scheduled/" + id + "/toggle", "/scheduled/" + id + "/cancel"} {
		r := s.post(path, nil)
		if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/scheduled" {
			t.Errorf("%s: %d %v", path, r.StatusCode, r.Header)
		}
	}
	refused := s.post("/scheduled", schedule("Bad", "p", "nonsense"))
	body := status(t, refused, http.StatusUnprocessableEntity)
	mustContain(t, body, "<html", "Not saved", "unrecognised schedule", `id="schedule-dialog"`)
	if refused.Header.Get("HX-Retarget") != "" {
		t.Errorf("a full page carries list headers: %v", refused.Header)
	}
	if strings.Count(body, `id="toaster"`) < 1 {
		t.Error("no toast on the refused full page")
	}
}
