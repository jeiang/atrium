package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/core/coremodule"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

func TestAppSubNavigationFollowsTheModuleFlag(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, allFlagsTrue())
	base := startServeIn(t, dir)
	issued := runApp(t, "auth", "token", "issue", "--name", "mail-navigation")
	if issued.code != cli.ExitOK {
		t.Fatalf("issue API token: %s", issued.stderr)
	}
	site := &apiSite{t: t, base: base}
	secret := strings.TrimSpace(issued.stdout)
	browser := webG3(t, base)
	mailSection := func() *web.ShellSection {
		t.Helper()
		got := site.do(http.MethodGet, "/api/v1/shell", secret, nil)
		if got.status != http.StatusOK {
			t.Fatalf("shell: %d %s", got.status, got.body)
		}
		var shell coremodule.Shell
		if err := json.Unmarshal([]byte(got.body), &shell); err != nil {
			t.Fatal(err)
		}
		for _, sec := range shell.Sections {
			if sec.Key == "mail_app" {
				return &sec
			}
		}
		return nil
	}

	sec := mailSection()
	if sec == nil || sec.Label != "Mail" || sec.URL != "/triage" || len(sec.Tabs) != 3 ||
		sec.Tabs[0].Label != "Decisions" || sec.Tabs[0].URL != "/triage" || sec.Tabs[1].Label != "Rules" || sec.Tabs[1].URL != "/rules" ||
		sec.Tabs[2].Label != "Status" || sec.Tabs[2].URL != "/live" {
		t.Fatalf("Mail section = %+v, want one Mail entry with Decisions, Rules and Status", sec)
	}
	mailReads := []string{"/web/v1/mail/triage", "/web/v1/mail/rules", "/web/v1/mail/live"}
	for _, path := range mailReads {
		if got := browser.do(http.MethodGet, path, nil); got.status != http.StatusOK {
			t.Fatalf("GET %s with Mail on = %d: %.400s", path, got.status, got.body)
		}
	}

	if got := runApp(t, "settings", "set", module.FlagKey(unit.Mail), "false"); got.code != cli.ExitOK {
		t.Fatalf("turn Mail off: %s", got.stderr)
	}
	waitFor(t, func() bool { return mailSection() == nil })
	for _, path := range mailReads {
		expectAPI(t, browser.do(http.MethodGet, path, nil), http.StatusConflict, "module_disabled")
	}
}
