package main

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

var builtinJobNames = []string{
	"nightly", "weekly-review", "daily-nudges", "task-reminders", "memory-work", "knowledge-export",
	"calendar-candidates", "media-channels", "media-sweep", "media-thumbnails", "package-tracking", "ops-sampler",
	"caldav", "carddav",
}

func schedulerSeededOnce(t *testing.T, d *db.DB) {
	t.Helper()
	var total int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM jobs WHERE kind IN ('builtin', 'nightly')").Scan(&total); err != nil || total != len(builtinJobNames) {
		t.Fatalf("%d built-in jobs (%v), want %d", total, err, len(builtinJobNames))
	}
	for _, name := range builtinJobNames {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM jobs WHERE name = ? AND kind IN ('builtin', 'nightly')", name).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s seeded %d times (%v), want once", name, n, err)
		}
	}
}

func schedulerJob(t *testing.T, d *db.DB, name string) (id int64, schedule, state string) {
	t.Helper()
	if err := d.Read.QueryRow("SELECT id, schedule, state FROM jobs WHERE name = ?", name).Scan(&id, &schedule, &state); err != nil {
		t.Fatalf("job %s: %v", name, err)
	}
	return id, schedule, state
}

func TestSchedulerRunsCatalogJobsFromEveryOwner(t *testing.T) {
	flags := flagsExcept("calendar", "false")
	flags["mail"] = "false"
	dir, api, f := moduleFixture(t, flags)
	asstWarm(t)
	d := asstDB(t, dir)
	if r := runSettings(t, "set", "agent_schedule_daily_nudges", "daily 07:10"); r.code != cli.ExitOK {
		t.Fatalf("set the nudges schedule: exit %d stderr %q", r.code, r.stderr)
	}
	schedulerSeed(t, f, d)
	schedulerSeededOnce(t, d)
	if _, schedule, _ := schedulerJob(t, d, "daily-nudges"); schedule != "daily 07:10" {
		t.Errorf("daily-nudges schedule = %q, want the operator's", schedule)
	}

	w := asstBrowse(t, startServeIn(t, dir))
	reminders, _, _ := schedulerJob(t, d, "task-reminders")
	code, body, _ := w.post("/scheduled/"+strconv.FormatInt(reminders, 10)+"/toggle", url.Values{}, true)
	w.want(code, http.StatusOK, body, "Paused")
	_, _, disabled := schedulerJob(t, d, "task-reminders")
	if disabled == "active" {
		t.Fatal("the toggle left task-reminders active")
	}

	api.Push(asstReply("dreamt"), asstReply("the week in review"))
	before := len(api.Requests())
	due := []string{"nightly", "weekly-review", "calendar-candidates", "package-tracking"}
	schedulerDue(t, d, due...)
	schedulerRun(t, f, d, schedulerTerminal(d, due...))

	for _, name := range []string{"nightly", "weekly-review"} {
		if status, text := schedulerStatus(t, d, name); status != "ok" {
			t.Errorf("%s = %s (%s), want ok", name, status, text)
		}
	}
	if got := len(api.Requests()) - before; got < 2 {
		t.Errorf("the nightly and weekly jobs sent %d provider requests, want at least 2", got)
	}
	for _, name := range []string{"calendar-candidates", "package-tracking"} {
		if status, text := schedulerStatus(t, d, name); status != "skipped_module" {
			t.Errorf("%s with its module disabled = %s (%s), want skipped_module", name, status, text)
		}
	}

	schedulerSeed(t, f, d)
	schedulerSeededOnce(t, d)
	if _, schedule, _ := schedulerJob(t, d, "daily-nudges"); schedule != "daily 07:10" {
		t.Errorf("a later start reset the daily-nudges schedule to %q", schedule)
	}
	if _, _, state := schedulerJob(t, d, "task-reminders"); state != disabled {
		t.Errorf("a later start changed task-reminders from %s to %s", disabled, state)
	}
}
