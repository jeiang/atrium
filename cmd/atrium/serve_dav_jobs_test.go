package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/unit"
)

var davJobKeys = []string{unit.Calendar + "/" + passrun.CalDAV, unit.Contacts + "/" + passrun.CardDAV}

type heldDAV struct {
	*httptest.Server
	hits    atomic.Int64
	release chan struct{}
}

func newHeldDAV(t *testing.T, upstream string) *heldDAV {
	t.Helper()
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	h := &heldDAV{release: make(chan struct{})}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		select {
		case <-h.release:
		case <-r.Context().Done():
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		h.open()
		h.Close()
	})
	return h
}

func (h *heldDAV) open() {
	select {
	case <-h.release:
	default:
		close(h.release)
	}
}

func davNextDue(t *testing.T, d *db.DB, key string) *float64 {
	t.Helper()
	var due *float64
	if err := d.Read.QueryRow("SELECT next_due FROM jobs WHERE declaration_key = ?", key).Scan(&due); err != nil {
		t.Fatalf("next due of %s: %v", key, err)
	}
	return due
}

func setDAVDue(t *testing.T, d *db.DB, due float64) {
	t.Helper()
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key IN (?, ?)", due, davJobKeys[0], davJobKeys[1])
}

func TestServeMirrorsCalDAVAndCardDAVOnSchedule(t *testing.T) {
	t.Parallel()
	dir, srv := davFixture(t)
	srv.SetObject(caldavtest.CalendarPath(0)+"dentist.ics", dentistICS)
	srv.SetObject(caldavtest.BookPath+"alice.vcf", aliceCard)
	held := newHeldDAV(t, srv.URL)
	setEnv(t, davmirror.CalDAVEnv, held.URL+"/")
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-3600, davJobKeys[0])

	started := float64(time.Now().Unix())
	base := startServeIn(t, dir)
	waitUntil(t, 30*time.Second, "serve to prime the DAV jobs", func() bool {
		for _, key := range davJobKeys {
			if due := davNextDue(t, d, key); due == nil || *due < started {
				return false
			}
		}
		return true
	})
	for _, key := range davJobKeys {
		due := davNextDue(t, d, key)
		if *due < started+55 || *due > float64(time.Now().Unix())+65 {
			t.Fatalf("%s next due %.0f s after serve start, want the 1 minute initial delay", key, *due-started)
		}
	}
	if n := schedulerRuns(t, d, passrun.CalDAV) + schedulerRuns(t, d, passrun.CardDAV); n != 0 || held.hits.Load() != 0 {
		t.Fatalf("%d DAV job runs and %d CalDAV requests at serve start, want the missed run skipped", n, held.hits.Load())
	}

	var jobID int64
	var schedule string
	if err := d.Read.QueryRow("SELECT id, schedule FROM jobs WHERE declaration_key = ?", davJobKeys[0]).Scan(&jobID, &schedule); err != nil {
		t.Fatal(err)
	}
	b := localWebG5(t, base)
	list := b.do(http.MethodGet, "/web/v1/schedules", nil)
	var jobs struct {
		Data struct {
			Items []struct {
				ID        int64 `json:"id"`
				Protected bool  `json:"protected"`
			} `json:"items"`
		} `json:"data"`
	}
	if list.status != http.StatusOK || !strings.Contains(list.body, string(passrun.CalDAV)) || json.Unmarshal([]byte(list.body), &jobs) != nil {
		t.Fatalf("schedules: %d %.300s", list.status, list.body)
	}
	listed := false
	for _, job := range jobs.Data.Items {
		if job.ID == jobID {
			listed = true
			if !job.Protected {
				t.Fatalf("the schedules list offers to edit the fixed CalDAV schedule: %.300s", list.body)
			}
		}
	}
	if !listed {
		t.Fatalf("schedules list lacks job %d: %.300s", jobID, list.body)
	}
	if edit := b.do(http.MethodPatch, fmt.Sprintf("/web/v1/schedules/%d", jobID), map[string]any{"schedule": "*/5 * * * *"}); edit.status != http.StatusConflict || !strings.Contains(edit.body, "fixed by its module") {
		t.Fatalf("fixed schedule edit = %d %.300s", edit.status, edit.body)
	}
	var after string
	if err := d.Read.QueryRow("SELECT schedule FROM jobs WHERE id = ?", jobID).Scan(&after); err != nil || after != schedule {
		t.Fatalf("CalDAV schedule %q after a refused edit (%v), want %q", after, err, schedule)
	}

	setDAVDue(t, d, float64(time.Now().Unix())-1)
	waitUntil(t, 60*time.Second, "the scheduled CalDAV run to reach the server", func() bool { return held.hits.Load() > 0 })
	manual := runApp(t, "calendar", "sync")
	if manual.code == cli.ExitOK || !strings.Contains(manual.stderr, "held by another invocation") {
		t.Fatalf("calendar sync beside the scheduled run = (%d, %q), want a held-lock refusal", manual.code, manual.stderr)
	}
	mirrored := filepath.Join(dir, "calendars", "cal0", "dentist.ics")
	if _, err := os.Stat(mirrored); !os.IsNotExist(err) {
		t.Fatalf("the mirror changed while the scheduled run was held: %v", err)
	}

	held.open()
	waitUntil(t, 60*time.Second, "both DAV job runs", schedulerTerminal(d, passrun.CalDAV, passrun.CardDAV))
	for _, name := range []string{passrun.CalDAV, passrun.CardDAV} {
		if status, text := schedulerStatus(t, d, name); status != "ok" {
			t.Fatalf("%s job status %s: %s", name, status, text)
		}
		if n := schedulerRuns(t, d, name); n != 1 {
			t.Fatalf("%d %s job runs, want 1", n, name)
		}
		if ok, failed, _ := passRows(t, dir, name); ok != 1 || failed != 0 {
			t.Fatalf("%s pass rows ok=%d failed=%d, want 1 and 0", name, ok, failed)
		}
	}
	if data, err := os.ReadFile(mirrored); err != nil || !strings.Contains(string(data), "SUMMARY:Dentist") {
		t.Fatalf("mirrored event %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "contacts", "card", "alice.vcf")); err != nil || !strings.Contains(string(data), "alice@example.com") {
		t.Fatalf("mirrored contact %q, %v", data, err)
	}
	for _, key := range davJobKeys {
		if due := davNextDue(t, d, key); due == nil || *due < started+14*60 || *due > float64(time.Now().Unix())+15*60+1 {
			t.Fatalf("%s next due %v after the run, want 15 minutes after its start", key, due)
		}
	}
}

func TestFailedDAVRunRetriesAfterItsBackoff(t *testing.T) {
	t.Parallel()
	dir, srv := davFixture(t)
	srv.SetObject(caldavtest.CalendarPath(0)+"dentist.ics", dentistICS)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFile(t, dir, mailAccount{"icloud", "icloud", "operator@icloud.invalid", "wrong", false}))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)

	for attempt, delay := range []float64{60, 120} {
		setDAVDue(t, d, float64(time.Now().Unix())-1)
		before := float64(time.Now().Unix())
		ran := runApp(t, "agent", "tick")
		for i, name := range []string{passrun.CalDAV, passrun.CardDAV} {
			if status, text := schedulerStatus(t, d, name); status != "error" || !strings.Contains(text, "401") {
				t.Fatalf("attempt %d: %s job status %s (%s), tick exit %d %q, want an error from the 401", attempt, name, status, text, ran.code, ran.stderr)
			}
			if due := davNextDue(t, d, davJobKeys[i]); due == nil || *due < before+delay-1 || *due > float64(time.Now().Unix())+delay+1 {
				t.Fatalf("attempt %d: %s next due %v, want %.0f s after the failure", attempt, name, due, delay)
			}
			if _, failed, _ := passRows(t, dir, name); failed != attempt*2+1 {
				t.Fatalf("attempt %d: %s failed pass rows %d, want %d", attempt, name, failed, attempt*2+1)
			}
		}
		if attempt == 0 {
			for i, command := range []string{"calendar", "contacts"} {
				due := *davNextDue(t, d, davJobKeys[i])
				if ran := runApp(t, command, "sync"); ran.code == cli.ExitOK || !strings.Contains(ran.stderr, "401") {
					t.Fatalf("manual %s sync = %d %s, want 401", command, ran.code, ran.stderr)
				}
				if got := davNextDue(t, d, davJobKeys[i]); got == nil || *got != due {
					t.Fatalf("manual failure changed %s retry from %f to %v", command, due, got)
				}
			}
		}
	}
	retries := []float64{*davNextDue(t, d, davJobKeys[0]), *davNextDue(t, d, davJobKeys[1])}
	serve := serveCLIProcess(t, dir)
	for i, key := range davJobKeys {
		if due := davNextDue(t, d, key); due == nil || *due != retries[i] {
			t.Fatalf("restart changed %s pending retry from %f to %v", key, retries[i], due)
		}
	}
	if code := serve.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Fatalf("serve exit %d: %s", code, serve.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "calendars", "cal0", "dentist.ics")); !os.IsNotExist(err) {
		t.Fatalf("a failed sync wrote the mirror: %v", err)
	}
}

func TestDAVJobsWithoutAnICloudLoginRecordNoPass(t *testing.T) {
	t.Parallel()
	dir, _ := davFixture(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFile(t, dir))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	setDAVDue(t, d, float64(time.Now().Unix())-1)
	if ran := runApp(t, "agent", "tick"); ran.code != cli.ExitOK {
		t.Fatalf("agent tick: exit %d %q", ran.code, ran.stderr)
	}
	if status, text := schedulerStatus(t, d, passrun.CardDAV); status != "skipped_unconfigured" || !strings.Contains(text, "iCloud") {
		t.Fatalf("carddav job status %s (%s), want skipped_unconfigured", status, text)
	}
	if status, _ := schedulerStatus(t, d, passrun.CalDAV); !strings.HasPrefix(status, "skipped_") {
		t.Fatalf("caldav job status %s, want a skip", status)
	}
	for _, name := range []string{passrun.CalDAV, passrun.CardDAV} {
		if ok, failed, _ := passRows(t, dir, name); ok+failed != 0 {
			t.Fatalf("%s pass rows ok=%d failed=%d for a skipped run, want none", name, ok, failed)
		}
	}
}
