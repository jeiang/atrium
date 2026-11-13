//go:build !modernc

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/passrun"
)

type watchReport struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Restarts int    `json:"restarts"`
}

func watchHealth(t *testing.T, opsBase string) map[string]watchReport {
	t.Helper()
	status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, opsBase+"/healthz", nil)
	if status != http.StatusOK {
		t.Fatalf("healthz = %d: %s", status, body)
	}
	var h struct {
		Services []watchReport `json:"services"`
	}
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatalf("healthz body %q: %v", body, err)
	}
	out := map[string]watchReport{}
	for _, svc := range h.Services {
		if strings.HasPrefix(svc.Name, "mail-watch/") {
			out[svc.Name] = svc
		}
	}
	return out
}

func watchRig(t *testing.T, creds map[string]string, accounts ...mailAccount) (string, map[string]*mailtest.FakeClient) {
	t.Helper()
	dir, embed := keyedMailFixture(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, embed, filepath.Join(testRoot, "host", "anthropic.key"), accounts...))
	fakes := map[string]*mailtest.FakeClient{}
	for _, name := range []string{"home", "work"} {
		f := mailtest.NewFakeClient()
		f.AddBox("INBOX", mailtest.NewBox(1))
		fakes[name] = f
	}
	strictIMAPRig(t, creds, fakes)
	return dir, fakes
}

func TestServeWatchesEachAccountAndRestartsOnlyTheDroppedOne(t *testing.T) {
	dir, fakes := watchRig(t, map[string]string{"me@home.test": "home-pass", "me@work.test": "work-pass"},
		mailAccount{"home", "icloud", "me@home.test", "home-pass", false}, mailAccount{"work", "icloud", "me@work.test", "work-pass", false})
	home, work := fakes["home"], fakes["work"]
	opsBase := opsAddr(t)
	_, logs, _ := startServeWith(t, dir, true)
	waitWatching(t, home, work)
	health := watchHealth(t, opsBase)
	if len(health) != 2 || health["mail-watch/home"].Status != "running" || health["mail-watch/work"].Status != "running" {
		t.Fatalf("healthz mail watches %+v, want home and work running", health)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := work.DropIdle(ctx); err != nil {
		t.Fatalf("drop the work IDLE connection: %v", err)
	}
	waitUntil(t, 60*time.Second, "the work watch to restart after its dropped connection", func() bool {
		return serviceFailures(logs, run.WatchServiceName("work")) == 1 && serviceStarts(logs, run.WatchServiceName("work")) == 2 && work.Idling() == 1
	})
	if !strings.Contains(logs.String(), "work connection lost") {
		t.Errorf("the dropped connection was not logged as lost: %s", logs.String())
	}
	if starts, failures := serviceStarts(logs, run.WatchServiceName("home")), serviceFailures(logs, run.WatchServiceName("home")); starts != 1 || failures != 0 {
		t.Fatalf("the home watch started %d times and failed %d times beside the work fault, want 1 and 0", starts, failures)
	}
	health = watchHealth(t, opsBase)
	if health["mail-watch/work"].Restarts != 1 || health["mail-watch/home"].Restarts != 0 || health["mail-watch/work"].Status != "running" {
		t.Fatalf("healthz mail watches after the fault %+v, want work restarted once and running, home untouched", health)
	}

	passes := strings.Count(logs.String(), "home: 0 new, 0 gone, 0 flag changes")
	if err := home.Wake(ctx); err != nil {
		t.Fatalf("wake the home IDLE: %v", err)
	}
	waitUntil(t, 60*time.Second, "the IDLE change to run a pass for home", func() bool {
		return strings.Count(logs.String(), "home: 0 new, 0 gone, 0 flag changes") > passes && home.Idling() == 1
	})
	if starts, failures := serviceStarts(logs, run.WatchServiceName("home")), serviceFailures(logs, run.WatchServiceName("home")); starts != 1 || failures != 0 {
		t.Fatalf("the home watch started %d times and failed %d times across its IDLE pass, want 1 and 0", starts, failures)
	}
	if len(home.Writes) != 0 || len(work.Writes) != 0 {
		t.Fatalf("shadow-mode watch passes wrote home %v work %v", home.Writes, work.Writes)
	}
}

func TestServeWatchFollowsAccountsChangedAtRuntime(t *testing.T) {
	dir, fakes := watchRig(t, map[string]string{"me@home.test": "home-pass", "me@work.test": "work-pass", "me@work2.test": "work2-pass"},
		mailAccount{"home", "icloud", "me@home.test", "home-pass", false})
	home, work := fakes["home"], fakes["work"]
	strictSMTPRig(t, map[string]string{"me@work.test": "work-pass", "me@work2.test": "work2-pass"})
	opsBase := opsAddr(t)
	base, logs, _ := startServeWith(t, dir, true)
	waitWatching(t, home)
	if health := watchHealth(t, opsBase); len(health) != 1 || health["mail-watch/home"].Status != "running" {
		t.Fatalf("healthz mail watches %+v, want only home", health)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/settings/mail", nil); code != http.StatusOK {
		t.Fatalf("mail settings: %d", code)
	}
	add := url.Values{"id": {"work"}, "label": {"Work"}, "preset": {"icloud"}, "login": {"me@work.test"}, "password": {"work-pass"}, "auth_method": {"password"}}
	if code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/mail/accounts", add); code != http.StatusOK {
		t.Fatalf("add work = %d: %.300s", code, body)
	}
	waitUntil(t, 60*time.Second, "the added account to get its watch", func() bool {
		return serviceStarts(logs, run.WatchServiceName("work")) == 1 && work.Idling() == 1
	})

	update := url.Values{"label": {"Work"}, "login": {"me@work2.test"}, "password": {"work2-pass"}, "enabled": {"on"}, "auth_method": {"password"}}
	if code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/mail/accounts/work", update); code != http.StatusOK {
		t.Fatalf("re-credential work = %d: %.300s", code, body)
	}
	waitUntil(t, 60*time.Second, "the re-credentialed account to restart its watch", func() bool {
		return serviceStarts(logs, run.WatchServiceName("work")) == 2 && work.Idling() == 1
	})
	if n := serviceFailures(logs, run.WatchServiceName("work")); n != 0 {
		t.Fatalf("the work watch failed %d times across the credential change, want a clean restart", n)
	}

	if code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/mail/accounts/work/remove", url.Values{}); code != http.StatusOK && code != http.StatusSeeOther {
		t.Fatalf("remove work = %d: %.300s", code, body)
	}
	waitUntil(t, 60*time.Second, "the removed account to lose its watch", func() bool {
		_, present := watchHealth(t, opsBase)["mail-watch/work"]
		return !present && work.Idling() == 0
	})
	if starts, failures := serviceStarts(logs, run.WatchServiceName("home")), serviceFailures(logs, run.WatchServiceName("home")); starts != 1 || failures != 0 {
		t.Fatalf("the home watch started %d times and failed %d times across work's changes, want 1 and 0", starts, failures)
	}
	if health := watchHealth(t, opsBase); health["mail-watch/home"].Status != "running" {
		t.Fatalf("healthz mail watches after the removal %+v, want home running", health)
	}
}

func TestServeWatchPassWaitsForTheScheduledPass(t *testing.T) {
	m := mailSafetyFixture(t, "live")
	d := mustConnect(t, m.path, keyFor(t))
	declareJobsAndParkThem(t, d)
	serveWatching(t, m.dir, d, m.fake)
	m.correctionPending(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "COPY" && armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return nil
	}
	t.Cleanup(func() {
		if !isClosed(release) {
			close(release)
		}
	})
	manual := syncDue(t, d)
	select {
	case <-entered:
	case <-time.After(60 * time.Second):
		t.Fatal("the scheduled pass never reached its COPY")
	}

	const fresh = "Mail arriving during the scheduled pass"
	m.add("INBOX", fresh, "someone@elsewhere.test", "during-pass", "Hello.")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.fake.Wake(ctx); err != nil {
		t.Fatalf("wake the IDLE: %v", err)
	}
	time.Sleep(3 * time.Second)
	if n := m.count(t, "SELECT COUNT(*) FROM messages WHERE subject = ?", fresh); n != 0 {
		t.Fatalf("the watch pass synced %d new messages while the scheduled pass held mail-pass", n)
	}
	close(release)
	waitUntil(t, 60*time.Second, "the watch pass after the scheduled pass", func() bool {
		return m.count(t, "SELECT COUNT(*) FROM messages WHERE subject = ?", fresh) == 1 && m.fake.Idling() == 1
	})
	waitUntil(t, 30*time.Second, "the scheduled mail sync run", scheduledRunEnded(d, passrun.Sync, manual))
	if status, text := schedulerStatus(t, d, passrun.Sync); status != "ok" {
		t.Fatalf("mail sync job status %s: %s", status, text)
	}
	if got := m.verbs(); !slices.Equal(got, []string{"COPY", "STORE", "EXPUNGE"}) {
		t.Fatalf("the scheduled and watch passes wrote %v, want one COPY, STORE, EXPUNGE", got)
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'"); state != "done" {
		t.Fatalf("the correction effect is %s, want done", state)
	}
	if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
		t.Fatalf("Bills holds %d messages, want 1", n)
	}
}

func TestServeDrainFinishesTheCurrentWatchPass(t *testing.T) {
	m := mailSafetyFixture(t, "live")
	d := mustConnect(t, m.path, keyFor(t))
	declareJobsAndParkThem(t, d)
	_, logs, stop := startServeWith(t, m.dir, true)
	waitWatching(t, m.fake)
	m.correctionPending(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "COPY" && armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return nil
	}
	t.Cleanup(func() {
		if !isClosed(release) {
			close(release)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.fake.Wake(ctx); err != nil {
		t.Fatalf("wake the IDLE: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(60 * time.Second):
		t.Fatal("the watch pass never reached its COPY")
	}

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("serve stopped while the watch pass was still writing")
	case <-time.After(2 * time.Second):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(60 * time.Second):
		t.Fatal("serve did not stop after the watch pass finished")
	}
	if got := m.verbs(); !slices.Equal(got, []string{"COPY", "STORE", "EXPUNGE"}) {
		t.Fatalf("the drained watch pass wrote %v, want COPY, STORE, EXPUNGE", got)
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'"); state != "done" {
		t.Fatalf("the correction effect is %s after drain, want done", state)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE state IN ('pending', 'unknown', 'running')"); n != 0 {
		t.Fatalf("%d unsettled effects after drain", n)
	}
	if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
		t.Fatalf("Bills holds %d messages, want 1", n)
	}
	if strings.Contains(logs.String(), "outlived") {
		t.Fatalf("drain reported work past its deadline: %s", logs.String())
	}
}

func TestWatchCommandRefusesBesideServe(t *testing.T) {
	m := mailSafetyFixture(t, "shadow")
	d := mustConnect(t, m.path, keyFor(t))
	declareJobsAndParkThem(t, d)
	serveWatching(t, m.dir, d, m.fake)
	ran := runApp(t, "watch")
	if ran.code == 0 || !strings.Contains(ran.stderr, "in use by a running atrium serve") {
		t.Fatalf("watch beside serve = (%d, %q), want a refusal naming the running serve", ran.code, ran.stderr)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM pass_runs"); n != 0 {
		t.Fatalf("the refused watch recorded %d pass runs", n)
	}
}
