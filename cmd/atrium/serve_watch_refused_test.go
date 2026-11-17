package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/imapclient"
	"github.com/jeiang/atrium/internal/apps/mail/mailtest"
	mailrun "github.com/jeiang/atrium/internal/apps/mail/run"
	"github.com/jeiang/atrium/internal/config"
)

func TestServeWatchWaitsAnHourAfterARefusedLogin(t *testing.T) {
	dir, embed := keyedMailFixture(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, embed, filepath.Join(testRoot, "host", "anthropic.key"),
		mailAccount{"home", "icloud", "me@home.test", "home-pass", false}, mailAccount{"work", "icloud", "me@work.test", "work-pass", false}))
	work := mailtest.NewFakeClient()
	work.AddBox("INBOX", mailtest.NewBox(1))
	t.Cleanup(imapclient.UseDialer(func(_ context.Context, a config.AccountSpec, _ string) (mail.Conn, error) {
		if a.Name == "home" {
			return nil, fmt.Errorf("%w: AUTHENTICATIONFAILED Invalid credentials", mail.ErrLoginRefused)
		}
		return work.Conn(), nil
	}))
	opsBase := opsAddr(t)
	_, logs, _ := startServeWith(t, dir, true)
	waitWatching(t, work)
	waitUntil(t, 30*time.Second, "the home watch to hold after its refused login", func() bool {
		return strings.Contains(logs.String(), "home login refused, retrying in 1h0m0s")
	})
	health := watchHealth(t, opsBase())
	if home := health["mail-watch/home"]; home.Status != "backoff" || home.Restarts != 1 {
		t.Fatalf("home watch after a refused login %+v, want one restart held in backoff", home)
	}
	if work := health["mail-watch/work"]; work.Status != "running" || work.Restarts != 0 {
		t.Fatalf("work watch beside the refused home login %+v, want running and untouched", work)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := work.Wake(ctx); err != nil {
		t.Fatalf("wake the work IDLE: %v", err)
	}
	waitUntil(t, 30*time.Second, "the work watch to finish an IDLE round trip", func() bool {
		return strings.Count(logs.String(), "work: 0 new, 0 gone, 0 flag changes") >= 2 && work.Idling() == 1
	})
	if starts := serviceStarts(logs, mailrun.WatchServiceName("home")); starts != 1 {
		t.Fatalf("the held home watch started %d times, want 1", starts)
	}
	if home := watchHealth(t, opsBase())["mail-watch/home"]; home.Status != "backoff" || home.Restarts != 1 {
		t.Fatalf("home watch after the work round trip %+v, want still held after one restart", home)
	}
}
