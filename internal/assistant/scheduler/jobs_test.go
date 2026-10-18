package scheduler_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

const tz = "America/Port_of_Spain"

var zoneLoc = func() *time.Location {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		panic(err)
	}
	return loc
}()

func local(y int, mo time.Month, d, h, mi int) float64 {
	return float64(time.Date(y, mo, d, h, mi, 0, 0, zoneLoc).Unix())
}

var now = local(2026, 10, 15, 12, 0)

func wantScheduleError(t *testing.T, err error, contains string) {
	t.Helper()
	var se scheduler.Error
	if !errors.As(err, &se) || !strings.Contains(string(se), contains) {
		t.Fatalf("error %v, want a schedule error containing %q", err, contains)
	}
}

func tx[T any](t *testing.T, d *db.DB, fn func(ctx context.Context, q *sql.Tx) (T, error)) (T, error) {
	t.Helper()
	var out T
	err := d.WithTx(t.Context(), func(q *sql.Tx) error {
		var err error
		out, err = fn(t.Context(), q)
		return err
	})
	return out, err
}

func create(t *testing.T, d *db.DB, n scheduler.NewJob) (int64, error) {
	t.Helper()
	return tx(t, d, func(ctx context.Context, q *sql.Tx) (int64, error) { return scheduler.CreateJob(ctx, q, n) })
}

func mustCreate(t *testing.T, d *db.DB, n scheduler.NewJob) int64 {
	t.Helper()
	id, err := create(t, d, n)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func update(t *testing.T, d *db.DB, id int64, u scheduler.JobUpdate) (*scheduler.Job, error) {
	t.Helper()
	return tx(t, d, func(ctx context.Context, q *sql.Tx) (*scheduler.Job, error) {
		return scheduler.UpdateJob(ctx, q, id, now, u)
	})
}

func cancel(t *testing.T, d *db.DB, id int64) error {
	t.Helper()
	_, err := tx(t, d, func(ctx context.Context, q *sql.Tx) (struct{}, error) {
		return struct{}{}, scheduler.CancelJob(ctx, q, id)
	})
	return err
}

func get(t *testing.T, d *db.DB, id int64) *scheduler.Job {
	t.Helper()
	j, err := scheduler.GetJob(t.Context(), d.Read, id)
	if err != nil || j == nil {
		t.Fatalf("GetJob(%d) = %v, %v", id, j, err)
	}
	return j
}

func brief(schedule string) scheduler.NewJob {
	return scheduler.NewJob{Name: "brief", Prompt: "Check the inbox.", Schedule: schedule, TZ: tz, Now: now - 86400*4}
}

func TestParseForms(t *testing.T) {
	for _, c := range []struct {
		schedule string
		want     float64
	}{
		{"daily 07:30", local(2026, 10, 16, 7, 30)},
		{"daily 12:30", local(2026, 10, 15, 12, 30)},
		{"weekly mon 09:00", local(2026, 10, 19, 9, 0)},
		{"weekly Thursday 13:00", local(2026, 10, 15, 13, 0)},
		{"0 9 * * 1-5", local(2026, 10, 16, 9, 0)},
		{"*/15 13 * * *", local(2026, 10, 15, 13, 0)},
		{"30 8 1 * *", local(2026, 11, 1, 8, 30)},
		{"0 8 * * 7", local(2026, 10, 18, 8, 0)},
		{"2026-11-02T09:00", local(2026, 11, 2, 9, 0)},
	} {
		s, err := scheduler.Parse(c.schedule, tz)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := s.NextAfter(now); !ok || got != c.want {
			t.Errorf("%s: NextAfter = %v, %v; want %v", c.schedule, got, ok, c.want)
		}
	}
	once, err := scheduler.Parse("2026-11-02T09:00", tz)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := once.NextAfter(local(2026, 11, 3, 0, 0)); ok || once.Recurring() {
		t.Errorf("one-shot fires after its moment or is recurring")
	}
}

func TestParseRejects(t *testing.T) {
	for _, bad := range []string{"", "daily 25:00", "weekly funday 09:00", "61 * * * *", "* * * *", "tomorrow-ish", "*/0 * * * *"} {
		_, err := scheduler.Parse(bad, tz)
		wantScheduleError(t, err, "")
	}
}

func TestMinIntervalAndFuture(t *testing.T) {
	for _, bad := range []string{"*/30 * * * *", "0,30 9 * * *", "* * * * *"} {
		_, _, err := scheduler.Validate(bad, tz, now, true)
		wantScheduleError(t, err, "at least")
	}
	for _, c := range []struct {
		schedule string
		enforce  bool
	}{{"0 * * * *", true}, {"*/30 * * * *", false}, {"2026-10-15T12:05", true}} {
		if _, _, err := scheduler.Validate(c.schedule, tz, now, c.enforce); err != nil {
			t.Errorf("%s: %v", c.schedule, err)
		}
	}
	_, _, err := scheduler.Validate("2026-10-01T09:00", tz, now, true)
	wantScheduleError(t, err, "future")
}

func TestMaxActiveJobs(t *testing.T) {
	d := dbtest.Schema(t)
	daily := func(name string) scheduler.NewJob {
		return scheduler.NewJob{Name: name, Prompt: "p", Schedule: "daily 07:00", TZ: tz, Now: now}
	}
	for i := range scheduler.MaxActiveJobs {
		mustCreate(t, d, daily(fmt.Sprintf("j%d", i)))
	}
	_, err := create(t, d, daily("x"))
	wantScheduleError(t, err, "at most")
	if err := cancel(t, d, 1); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, d, daily("x"))
}

func TestCreateRequiresNameAndPrompt(t *testing.T) {
	d := dbtest.Schema(t)
	n := brief("daily 07:00")
	n.Prompt = " \t"
	_, err := create(t, d, n)
	wantScheduleError(t, err, "name and prompt are required")
	n = brief("  daily 07:00 ")
	n.Name, n.Prompt = "  brief ", " Check. "
	j := get(t, d, mustCreate(t, d, n))
	if j.Name != "brief" || j.Prompt != "Check." || j.Schedule != "daily 07:00" || j.Kind != scheduler.JobCron ||
		j.CreatedBy != scheduler.Operator || j.Effort != "high" || !j.Enabled || j.PromptBy != nil {
		t.Errorf("created job %+v", j)
	}
	if j.NextDue == nil || *j.NextDue != local(2026, 10, 12, 7, 0) {
		t.Errorf("next_due %v, want %v", j.NextDue, local(2026, 10, 12, 7, 0))
	}
}

func TestPromptBy(t *testing.T) {
	d := dbtest.Schema(t)
	const urlPrompt = "Read https://docs.test/brief and summarize it."
	job := func(name, createdBy, promptBy string) scheduler.NewJob {
		return scheduler.NewJob{Name: name, Prompt: urlPrompt, Schedule: "2026-10-15T12:30", TZ: tz, Now: now, CreatedBy: createdBy, PromptBy: promptBy}
	}
	byOperator := get(t, d, mustCreate(t, d, job("op", "", scheduler.Operator)))
	if byOperator.PromptBy == nil || *byOperator.PromptBy != scheduler.Operator {
		t.Errorf("operator prompt_by %v", byOperator.PromptBy)
	}
	byAgent := get(t, d, mustCreate(t, d, job("ag", scheduler.Agent, scheduler.Agent)))
	if byAgent.PromptBy == nil || *byAgent.PromptBy != scheduler.Agent || byAgent.CreatedBy != scheduler.Agent {
		t.Errorf("agent job %+v", byAgent)
	}
	if reminder := get(t, d, mustCreate(t, d, job("Reminder: x", "", ""))); reminder.PromptBy != nil {
		t.Errorf("reminder prompt_by %v", *reminder.PromptBy)
	}
	rewritten := mustCreate(t, d, job("rw", "", scheduler.Operator))
	j, err := update(t, d, rewritten, scheduler.JobUpdate{Prompt: new(urlPrompt), PromptBy: scheduler.Agent})
	if err != nil || j.PromptBy == nil || *j.PromptBy != scheduler.Agent {
		t.Fatalf("agent rewrite: %+v, %v", j, err)
	}
	j, err = update(t, d, rewritten, scheduler.JobUpdate{Prompt: new(urlPrompt), PromptBy: scheduler.Operator})
	if err != nil || j.PromptBy == nil || *j.PromptBy != scheduler.Operator {
		t.Fatalf("operator rewrite: %+v, %v", j, err)
	}
	j, err = update(t, d, rewritten, scheduler.JobUpdate{Schedule: new("daily 09:00")})
	if err != nil || j.PromptBy == nil || *j.PromptBy != scheduler.Operator {
		t.Fatalf("schedule change: %+v, %v", j, err)
	}
	j, err = update(t, d, rewritten, scheduler.JobUpdate{Prompt: new("Check mail.")})
	if err != nil || j.PromptBy != nil {
		t.Fatalf("prompt without author: %+v, %v", j, err)
	}
}

func TestUpdateCancelAndBuiltinsProtected(t *testing.T) {
	d := dbtest.Schema(t)
	nightly := mustCreate(t, d, scheduler.NewJob{
		Name: "nightly", Prompt: "Nightly consolidation.", Schedule: "daily 02:30", TZ: tz, Now: now,
		Kind: scheduler.JobNightly, SkipLimits: true,
	})
	wantScheduleError(t, cancel(t, d, nightly), "built-in runs cannot be cancelled; disable them instead")
	id := mustCreate(t, d, brief("daily 07:00"))
	j, err := update(t, d, id, scheduler.JobUpdate{Schedule: new("weekly mon 09:00")})
	if err != nil || *j.NextDue != local(2026, 10, 19, 9, 0) {
		t.Fatalf("schedule update: %+v, %v", j, err)
	}
	if j, err = update(t, d, id, scheduler.JobUpdate{Enabled: new(false)}); err != nil || j.Enabled {
		t.Fatalf("disable: %+v, %v", j, err)
	}
	j, err = update(t, d, id, scheduler.JobUpdate{Schedule: new("daily 08:00")})
	if err != nil || *j.NextDue != local(2026, 10, 19, 9, 0) || j.Schedule != "daily 08:00" {
		t.Fatalf("schedule update while disabled: %+v, %v", j, err)
	}
	if j, err = update(t, d, id, scheduler.JobUpdate{Enabled: new(true)}); err != nil || !j.Enabled || *j.NextDue != local(2026, 10, 16, 8, 0) {
		t.Fatalf("enable: %+v, %v", j, err)
	}
	_, err = update(t, d, id, scheduler.JobUpdate{Name: new(" ")})
	wantScheduleError(t, err, "name is empty")
	_, err = update(t, d, id, scheduler.JobUpdate{Prompt: new("")})
	wantScheduleError(t, err, "prompt is empty")
	_, err = update(t, d, id, scheduler.JobUpdate{Schedule: new("*/30 * * * *")})
	wantScheduleError(t, err, "at least")
	if err := cancel(t, d, id); err != nil {
		t.Fatal(err)
	}
	_, err = update(t, d, id, scheduler.JobUpdate{Name: new("x")})
	wantScheduleError(t, err, fmt.Sprintf("no active schedule %d", id))
	wantScheduleError(t, cancel(t, d, id), fmt.Sprintf("no active schedule %d", id))
	wantScheduleError(t, cancel(t, d, 999), "no active schedule 999")
}

func TestEnableRespectsCap(t *testing.T) {
	d := dbtest.Schema(t)
	paused := mustCreate(t, d, brief("daily 07:00"))
	if _, err := update(t, d, paused, scheduler.JobUpdate{Enabled: new(false)}); err != nil {
		t.Fatal(err)
	}
	for i := range scheduler.MaxActiveJobs {
		n := brief("daily 07:00")
		n.Name = fmt.Sprintf("j%d", i)
		mustCreate(t, d, n)
	}
	_, err := update(t, d, paused, scheduler.JobUpdate{Enabled: new(true)})
	wantScheduleError(t, err, "at most 20 active schedules; cancel one first")
	if j, err := update(t, d, paused, scheduler.JobUpdate{Enabled: new(true), SkipLimits: true}); err != nil || !j.Enabled {
		t.Fatalf("enable without limits: %+v, %v", j, err)
	}
}

func TestReminderBypassesLimitsAndCancels(t *testing.T) {
	d := dbtest.Schema(t)
	for i := range scheduler.MaxActiveJobs {
		n := brief("daily 07:00")
		n.Name = fmt.Sprintf("j%d", i)
		mustCreate(t, d, n)
	}
	for _, at := range []float64{local(2026, 10, 16, 9, 0), now + 300} {
		moment := time.Unix(int64(at), 0).In(zoneLoc).Format("2006-01-02T15:04:05-07:00")
		id := mustCreate(t, d, scheduler.NewJob{
			Name: "Reminder: Send the itinerary", Prompt: "Remind the operator: Send the itinerary (source mail 7)",
			Schedule: moment, TZ: tz, Now: now, SkipLimits: true,
		})
		j := get(t, d, id)
		if j.Kind != scheduler.JobCron || !j.Enabled || j.NextDue == nil || *j.NextDue != at || j.Schedule != moment {
			t.Fatalf("reminder %s: %+v", moment, j)
		}
		if err := cancel(t, d, id); err != nil {
			t.Fatal(err)
		}
		if j := get(t, d, id); !j.Cancelled || j.Enabled || j.NextDue != nil {
			t.Errorf("cancelled reminder %+v", j)
		}
	}
	listed, err := scheduler.ListJobs(t.Context(), d.Read, false)
	if err != nil || len(listed) != scheduler.MaxActiveJobs {
		t.Fatalf("ListJobs without cancelled: %d, %v", len(listed), err)
	}
	all, err := scheduler.ListJobs(t.Context(), d.Read, true)
	if err != nil || len(all) != scheduler.MaxActiveJobs+2 {
		t.Fatalf("ListJobs with cancelled: %d, %v", len(all), err)
	}
}

func TestListJobsOrderAndLastRun(t *testing.T) {
	d := dbtest.Schema(t)
	cron := mustCreate(t, d, brief("daily 07:00"))
	builtin := mustCreate(t, d, scheduler.NewJob{Name: "media-sweep", Prompt: "p", Schedule: "*/10 * * * *", TZ: tz, Now: now, Kind: scheduler.JobBuiltin, SkipLimits: true})
	nightly := mustCreate(t, d, scheduler.NewJob{Name: "nightly", Prompt: "p", Schedule: "daily 02:30", TZ: tz, Now: now, Kind: scheduler.JobNightly, SkipLimits: true})
	err := d.WithTx(t.Context(), func(q *sql.Tx) error {
		res, err := q.Exec("INSERT INTO job_runs (job_id, thread_id, due_at, started_at, status, error) VALUES (?, 4, ?, ?, 'error', 'boom')", cron, now, now+1)
		if err != nil {
			return err
		}
		run, err := res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = q.Exec("UPDATE jobs SET last_run_id = ? WHERE id = ?", run, cron)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := scheduler.ListJobs(t.Context(), d.Read, false)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, j := range listed {
		ids = append(ids, j.ID)
	}
	if fmt.Sprint(ids) != fmt.Sprint([]int64{nightly, cron, builtin}) {
		t.Errorf("order %v, want nightly, cron, builtin", ids)
	}
	c := listed[1]
	if c.LastStatus == nil || *c.LastStatus != "error" || *c.LastThreadID != 4 || *c.LastStarted != now+1 || *c.LastError != "boom" {
		t.Errorf("last run of %+v", c)
	}
	if listed[0].LastStatus != nil {
		t.Errorf("nightly has a last run %+v", listed[0])
	}
}
