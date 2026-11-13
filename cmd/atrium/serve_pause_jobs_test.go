package main

import (
	"os"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/passrun"
)

func TestLivePauseKeepsMirrorsAndBackupsRunning(t *testing.T) {
	fake := newHeldNotion(t, false)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	dest := t.TempDir()
	host := notionHostConfig(t, dir, fake.URL, "ntn_good")
	f, err := os.OpenFile(host, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n[core]\nbackup_directory = \"" + dest + "\"\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	if got := runApp(t, "live", "pause"); got.code != cli.ExitOK {
		t.Fatalf("live pause exit %d: %s", got.code, got.stderr)
	}
	due := float64(time.Now().Unix()) - 1
	for _, key := range []string{notion.JobKey, backup.JobKey, module.Mail + "/" + tracking.JobPackageTracking} {
		asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", due, key)
	}
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE name = ?", due, assistant.NightlyName)

	if ran := runApp(t, "agent", "tick"); ran.code != cli.ExitOK {
		t.Fatalf("agent tick while paused: exit %d: %s", ran.code, ran.stderr)
	}
	for name, want := range map[string]string{
		passrun.NotionSync:          "ok",
		passrun.Backup:              "ok",
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
