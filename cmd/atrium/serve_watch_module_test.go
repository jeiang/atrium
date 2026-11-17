package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	mailrun "github.com/jeiang/atrium/internal/apps/mail/run"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

func TestServeWatchStopsWithTheMailModuleAndStaysStopped(t *testing.T) {
	dir, fakes := watchRig(t, map[string]string{"me@home.test": "home-pass", "me@work.test": "work-pass"},
		mailAccount{"home", "icloud", "me@home.test", "home-pass", false}, mailAccount{"work", "icloud", "me@work.test", "work-pass", false})
	home, work := fakes["home"], fakes["work"]
	opsBase := opsAddr(t)
	base, logs, _ := startServeWith(t, dir, true)
	waitWatching(t, home, work)

	asstSet(t, module.FlagKey(unit.Mail), "false")
	waitUntil(t, 60*time.Second, "the mail watches to stop with the module", func() bool {
		return len(watchHealth(t, opsBase())) == 0 && home.Idling() == 0 && work.Idling() == 0
	})
	passes := strings.Count(logs.String(), ": 0 new, 0 gone")

	if got := webAsG4(t, base, nil).do(http.MethodDelete, "/web/v1/connections/mail/work", nil); got.status != http.StatusNoContent {
		t.Fatalf("disconnect work = %d: %.300s", got.status, got.body)
	}
	time.Sleep(3 * time.Second)
	if health := watchHealth(t, opsBase()); len(health) != 0 {
		t.Fatalf("healthz mail watches %+v after an account change with mail off, want none", health)
	}
	if home.Idling() != 0 || work.Idling() != 0 {
		t.Fatalf("IDLE sessions home %d work %d with mail off, want none", home.Idling(), work.Idling())
	}
	if starts := serviceStarts(logs, mailrun.WatchServiceName("home")); starts != 1 {
		t.Fatalf("the home watch started %d times, want only the start before the module was disabled", starts)
	}
	if n := strings.Count(logs.String(), ": 0 new, 0 gone"); n != passes {
		t.Fatalf("%d mail passes ran after the module was disabled, want none", n-passes)
	}
	if len(home.Writes) != 0 || len(work.Writes) != 0 {
		t.Fatalf("writes with mail off: home %v work %v", home.Writes, work.Writes)
	}
}
