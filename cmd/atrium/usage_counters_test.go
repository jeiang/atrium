package main

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/passrun"
)

func scrapeValue(t *testing.T, body, series string) float64 {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		if value, ok := strings.CutPrefix(line, series+" "); ok {
			n, err := strconv.ParseFloat(value, 64)
			if err != nil {
				t.Fatalf("%s value %q: %v", series, value, err)
			}
			return n
		}
	}
	return -1
}

func scrape(t *testing.T, opsBase string) string {
	t.Helper()
	status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, opsBase+"/metrics", nil)
	if status != http.StatusOK {
		t.Fatalf("ops /metrics = %d", status)
	}
	return body
}

func TestSchedulerJobRunCounterSurvivesARestart(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	dest := t.TempDir()
	setEnv(t, "ATRIUM_HOST_CONFIG", backupHost(t, dir, "backup_directory = \""+dest+"\""))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	now := float64(time.Now().Unix())
	asstExec(t, d, "INSERT INTO jobs (id, name, prompt, schedule, tz, kind, state, created_by, created_at, module, activated_at) VALUES (9001, 'Water the plants', 'remind me', '0 9 * * *', 'UTC', 'cron', 'disabled', 'operator', ?, 'assistant', ?)", now, now)
	asstExec(t, d, "INSERT INTO job_runs (job_id, due_at, started_at, ended_at, status) VALUES (9001, ?, ?, ?, 'ok')", now-60, now-60, now-30)
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", now-60, backup.JobKey)
	opsBase := opsAddr(t)
	series := `atrium_scheduler_job_runs_total{scheduled_job="` + passrun.Backup + `",status="ok"}`

	_, _, stop := startServeStoppable(t, dir)
	waitUntil(t, 60*time.Second, "the first backup run", schedulerTerminal(d, passrun.Backup))
	body := scrape(t, opsBase)
	before := scrapeValue(t, body, series)
	if before < 1 {
		t.Fatalf("%s = %v after one backup run, want at least 1:\n%s", series, before, body)
	}
	if got := scrapeValue(t, body, `atrium_scheduler_job_runs_total{scheduled_job="operator_schedule",status="ok"}`); got != 1 {
		t.Errorf("operator schedule runs = %v, want 1 under the shared label", got)
	}
	if strings.Contains(body, "Water the plants") {
		t.Error("an operator schedule name appears as a label value")
	}
	stop()

	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-1, backup.JobKey)
	startServeIn(t, dir)
	waitUntil(t, 60*time.Second, "the second backup run", func() bool {
		return schedulerRuns(t, d, passrun.Backup) == 2 && schedulerTerminal(d, passrun.Backup)()
	})
	if after := scrapeValue(t, scrape(t, opsBase), series); after != before+1 {
		t.Fatalf("%s = %v after a restart and one more run, want %v", series, after, before+1)
	}
}
