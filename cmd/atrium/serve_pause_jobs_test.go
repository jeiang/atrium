package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/passrun"
)

func TestLivePauseKeepsMirrorsAndBackupsRunning(t *testing.T) {
	fake := newHeldNotion(t, false)
	dir, srv := davFixture(t)
	srv.SetObject(caldavtest.CalendarPath(0)+"dentist.ics", dentistICS)
	srv.SetObject(caldavtest.BookPath+"alice.vcf", aliceCard)
	dest := t.TempDir()
	base, err := os.ReadFile(defaultHostConfig)
	if err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(dir, "notion.token")
	if err := os.WriteFile(token, []byte("ntn_good\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(dir, "host-paused.toml")
	body := fmt.Sprintf("%s\n[connections.\"service/notion\"]\nkind = \"service/notion\"\nlabel = \"Notion\"\nconfig = { api = %q }\ncapabilities = [\"api\"]\ncredential_files = { token = %q }\n\n[core]\nbackup_directory = %q\n", base, fake.URL, token, dest)
	if err := os.WriteFile(host, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	if got := runApp(t, "live", "pause"); got.code != cli.ExitOK {
		t.Fatalf("live pause exit %d: %s", got.code, got.stderr)
	}
	due := float64(time.Now().Unix()) - 1
	for _, key := range []string{notion.JobKey, backup.JobKey, davJobKeys[0], davJobKeys[1], module.Mail + "/" + tracking.JobPackageTracking} {
		asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", due, key)
	}
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE name = ?", due, assistant.NightlyName)

	if ran := runApp(t, "agent", "tick"); ran.code != cli.ExitOK {
		t.Fatalf("agent tick while paused: exit %d: %s", ran.code, ran.stderr)
	}
	for name, want := range map[string]string{
		passrun.NotionSync:          "ok",
		passrun.Backup:              "ok",
		passrun.CalDAV:              "ok",
		passrun.CardDAV:             "ok",
		tracking.JobPackageTracking: "skipped_pause",
		assistant.NightlyName:       "skipped_pause",
	} {
		if status, text := schedulerStatus(t, d, name); status != want {
			t.Errorf("%s status %s (%s) while paused, want %s", name, status, text, want)
		}
	}
	if fake.hits.Load() == 0 {
		t.Error("the paused Notion mirror never reached Notion")
	}
	if ok, failed, _ := passRows(t, dir, passrun.NotionSync); ok != 1 || failed != 0 {
		t.Errorf("notion-sync pass rows ok=%d failed=%d while paused, want 1 and 0", ok, failed)
	}
	for _, name := range []string{passrun.CalDAV, passrun.CardDAV} {
		if ok, failed, _ := passRows(t, dir, name); ok != 1 || failed != 0 {
			t.Errorf("%s pass rows ok=%d failed=%d while paused, want 1 and 0", name, ok, failed)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "calendars", "cal0", "dentist.ics")); err != nil || !strings.Contains(string(data), "SUMMARY:Dentist") {
		t.Errorf("event mirrored while paused %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "contacts", "card", "alice.vcf")); err != nil || !strings.Contains(string(data), "alice@example.com") {
		t.Errorf("contact mirrored while paused %q, %v", data, err)
	}
	if archives := backupArchives(t, dest); len(archives) != 1 {
		t.Errorf("archives while paused %v, want 1", archives)
	}
	stale, err := passrun.StaleJobs(t.Context(), d.Read)
	if err != nil {
		t.Fatal(err)
	}
	if why, found := stale[passrun.NotionSync]; found {
		t.Errorf("notion-sync stale while paused: %s", why)
	}
}
