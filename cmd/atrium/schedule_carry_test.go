package main

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/timex"
)

func TestTunedNightlyScheduleCarriesToTheJobRowOnce(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	d := asstDB(t, dir)
	asstExec(t, d, "INSERT INTO settings (key, value) VALUES ('agent_schedule_nightly', 'daily 04:10')")
	setEnv(t, "ATRIUM_ASSISTANT_AGENT_SCHEDULE_NIGHTLY", "daily 05:00")
	logs := &lockedBuffer{}
	previous := slog.Default()
	logx.Setup(logs)
	t.Cleanup(func() { slog.SetDefault(previous) })

	nightly := func() (string, time.Time) {
		t.Helper()
		var schedule, tz string
		var due *float64
		if err := d.Read.QueryRow("SELECT schedule, tz, next_due FROM jobs WHERE declaration_key = 'assistant/nightly'").Scan(&schedule, &tz, &due); err != nil {
			t.Fatalf("the nightly job: %v", err)
		}
		if due == nil {
			t.Fatal("the nightly job has no next run")
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			t.Fatal(err)
		}
		sec, us := timex.SplitTimestamp(*due)
		return schedule, time.Unix(sec, us*1000).In(loc)
	}
	carries := func() int {
		t.Helper()
		return asstCount(t, d, "SELECT COUNT(*) FROM operation_audit WHERE operation = 'scheduler.schedule.carry' AND target = 'job:assistant/nightly' AND source = 'migration'")
	}
	check := func(when string) {
		t.Helper()
		schedule, next := nightly()
		if schedule != "daily 04:10" || next.Hour() != 4 || next.Minute() != 10 {
			t.Errorf("%s: nightly schedule %q next %s, want the tuned daily 04:10", when, schedule, next)
		}
		if n := carries(); n != 1 {
			t.Errorf("%s: %d carry audit rows, want exactly 1", when, n)
		}
	}

	schedulerSeed(t, f, d)
	check("first start")
	if !strings.Contains(logs.String(), "ignoring a removed setting") || !strings.Contains(logs.String(), "ATRIUM_ASSISTANT_AGENT_SCHEDULE_NIGHTLY") {
		t.Errorf("the pinned schedule variable was not warned about: %q", logs.String())
	}

	schedulerSeed(t, f, d)
	check("second start")

	asstExec(t, d, "DELETE FROM settings WHERE key = 'agent_schedule_nightly'")
	schedulerSeed(t, f, d)
	check("after the setting row is gone")

	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "host.toml", "[assistant]\nagent_schedule_nightly = \"daily 05:00\"\n"))
	if r := runSettings(t, "list"); r.code != cli.ExitError || !strings.Contains(r.stderr, `unknown host key "assistant"`) {
		t.Errorf("a host file pin of the retired key: exit %d stderr %q, want the unknown host key refusal", r.code, r.stderr)
	}
}
