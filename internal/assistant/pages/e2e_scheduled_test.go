package pages

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type jobRow struct {
	id                  int64
	name, prompt, sched string
	state               string
}

func (s *site) job(name string) jobRow {
	s.t.Helper()
	var j jobRow
	if err := s.db.Read.QueryRow("SELECT id, name, prompt, schedule, state FROM jobs WHERE name = ?", name).
		Scan(&j.id, &j.name, &j.prompt, &j.sched, &j.state); err != nil {
		s.t.Fatalf("job %q: %v", name, err)
	}
	return j
}

func TestScheduledListToggleDelete(t *testing.T) {
	s := newSite(t)
	page := status(t, s.get("/scheduled"), http.StatusOK)
	mustContain(t, page, "nightly", "Every day at 02:30", "Fri 29 May 02:30", "weekly-review", "Sun 31 May 18:00", "No runs yet.", "America/Port_of_Spain", `id="scheduled-list"`, "Never")
	mustNotContain(t, page, "New schedule", "schedule-dialog", "/scheduled/new", "job-edit-", "Times are", "create one here")
	nightlyID := fmt.Sprint(s.job("nightly").id)
	mustContain(t, page, `id="job-toggle-`+nightlyID+`"`, `id="schedules-h"`, `data-focus-fallback`)
	mustNotContain(t, page, `id="job-delete-`+nightlyID+`"`, fmt.Sprintf(`id="job-delete-%d"`, s.job("weekly-review").id))
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
	mustNotContain(t, partial, "<html")

	for _, path := range []string{"/scheduled/new", "/scheduled/1/edit"} {
		if r := s.get(path); r.StatusCode != http.StatusNotFound && r.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: %d, want 404 or 405", path, r.StatusCode)
		}
	}
	for _, path := range []string{"/scheduled", "/scheduled/1"} {
		if r := s.post(path, nil); r.StatusCode != http.StatusNotFound && r.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: %d, want 404 or 405", path, r.StatusCode)
		}
	}

	s.exec("INSERT INTO jobs (name, prompt, schedule, tz, state, created_by, created_at) VALUES ('Digest', 'Summarize mail.', 'weekly mon 08:00', 'UTC', 'active', 'agent', 1)")
	id := fmt.Sprint(s.job("Digest").id)
	listed := status(t, s.get("/scheduled"), http.StatusOK)
	mustContain(t, listed, "Digest", "Every Monday at 08:00", "Summarize mail.", `id="job-delete-`+id+`"`, `hx-post="/scheduled/`+id+`/cancel"`, `hx-confirm="Delete Digest?"`, `aria-label="Delete Digest"`)

	toggled := s.post("/scheduled/"+id+"/toggle", nil, hx, "true")
	mustContain(t, status(t, toggled, http.StatusOK), "Schedule updated", "Paused", `id="scheduled-list"`)
	mustNotContain(t, toggled.body, "<html")
	if s.job("Digest").state == "active" {
		t.Error("toggle did not disable the job")
	}
	s.post("/scheduled/"+id+"/toggle", nil, hx, "true")
	if s.job("Digest").state != "active" {
		t.Error("second toggle did not enable the job")
	}
	s.exec("INSERT INTO jobs (name, prompt, schedule, tz, state, created_by, created_at) VALUES ('Past', 'p', '2020-01-01T09:00', 'UTC', 'disabled', 'operator', 1)")
	past := s.post(fmt.Sprintf("/scheduled/%d/toggle", s.job("Past").id), nil, hx, "true")
	mustContain(t, status(t, past, http.StatusUnprocessableEntity), "Not saved", `id="scheduled-list"`)

	cancelled := s.post("/scheduled/"+id+"/cancel", nil, hx, "true")
	body := status(t, cancelled, http.StatusOK)
	mustContain(t, body, "Schedule deleted")
	mustNotContain(t, body, "Digest")
	if s.job("Digest").state != "cancelled" {
		t.Error("cancel did not cancel the job")
	}
	status(t, s.post("/scheduled/"+id+"/toggle", nil), http.StatusNotFound)
	status(t, s.post("/scheduled/999/cancel", nil), http.StatusNotFound)
	mustContain(t, status(t, s.post("/scheduled/x/toggle", nil), http.StatusUnprocessableEntity), `"int_parsing"`, `"job_id"`)
	status(t, s.get("/settings/schedules"), http.StatusNotFound)

	builtin := s.job("weekly-review")
	refusedCancel := s.post(fmt.Sprintf("/scheduled/%d/cancel", builtin.id), nil, hx, "true")
	mustContain(t, status(t, refusedCancel, http.StatusUnprocessableEntity), "built-in runs cannot be cancelled; disable them instead", "Not saved")
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
	for _, state := range []string{"missed_coalesced", "skipped_pause", "skipped_budget", "running", "weird"} {
		s.exec("INSERT INTO job_runs (job_id, due_at, status) VALUES (?, 1780007200, ?)", nightly.id, state)
	}
	s.exec("UPDATE jobs SET last_run_id = ? WHERE id = ?", last, nightly.id)
	s.exec("INSERT INTO jobs (name, prompt, schedule, tz, next_due, created_by, created_at) VALUES ('Once', 'p', '2030-01-01T10:00', 'UTC', 1893492000, 'agent', 1)")
	s.exec("INSERT INTO jobs (name, prompt, schedule, tz, next_due, kind, created_by, created_at) VALUES ('Operator', 'p', '2030-01-01T10:00', 'UTC', 1893492000, 'cron', 'operator', 1)")

	page := status(t, s.get("/scheduled"), http.StatusOK)
	mustContain(t, page, "Thu 28 May 16:26", "Thu 28 May 17:26", "$0.123", fmt.Sprintf(`href="/chat/%d"`, thread), "Budget &lt;exceeded&gt;",
		`data-variant="destructive" title="Budget &lt;exceeded&gt;">Failed</span>`, "Completed", "Ran late", "Skipped (paused)", "Skipped (budget)", "Running",
		`title="The run was due while atrium was down; missed runs were merged into one."`, `title="Live mode was paused, so this run did not start."`,
		`title="The monthly budget was reached, so this run did not start."`, "Once on Tue 01 Jan 2030 at 10:00", "Tue 01 Jan 06:00",
		"Nightly", "Built-in", "Created by you", "Created by the assistant", `data-variant="outline">weird</span>`)
	mustNotContain(t, page, "Budget <exceeded>", `>ok</span>`, `>error</span>`, `>missed_coalesced</span>`, `>skipped_pause</span>`, `>skipped_budget</span>`, `>running</span>`)

	wide := status(t, s.get("/scheduled", hx, "true"), http.StatusOK)
	mustContain(t, wide, `<span class="font-medium">nightly</span>`)
}

func TestScheduledWithoutScriptAnswersWholePages(t *testing.T) {
	s := newSite(t)
	s.exec("INSERT INTO jobs (name, prompt, schedule, tz, state, created_by, created_at) VALUES ('Plain', 'p', 'daily 08:00', 'UTC', 'active', 'operator', 1)")
	id := fmt.Sprint(s.job("Plain").id)
	for _, path := range []string{"/scheduled/" + id + "/toggle", "/scheduled/" + id + "/cancel"} {
		r := s.post(path, nil)
		if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/scheduled" {
			t.Errorf("%s: %d %v", path, r.StatusCode, r.Header)
		}
	}
	s.exec("INSERT INTO jobs (name, prompt, schedule, tz, state, created_by, created_at) VALUES ('Past', 'p', '2020-01-01T09:00', 'UTC', 'disabled', 'operator', 1)")
	refused := s.post(fmt.Sprintf("/scheduled/%d/toggle", s.job("Past").id), nil)
	body := status(t, refused, http.StatusUnprocessableEntity)
	mustContain(t, body, "<html", "Not saved", `id="scheduled-list"`)
	if refused.Header.Get("HX-Retarget") != "" {
		t.Errorf("a full page carries list headers: %v", refused.Header)
	}
	if got := strings.Count(body, `id="toaster"`); got != 1 {
		t.Errorf("toaster count %d, want 1", got)
	}
}
