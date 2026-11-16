package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/core/coremodule"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

func TestAppSubNavigationFollowsTheModuleFlag(t *testing.T) {
	dir, _, _ := moduleFixture(t, allFlagsTrue())
	base := startServeIn(t, dir)
	issued := runApp(t, "auth", "token", "issue", "--name", "mail-navigation")
	if issued.code != cli.ExitOK {
		t.Fatalf("issue API token: %s", issued.stderr)
	}
	site := &apiSite{t: t, base: base}
	secret := strings.TrimSpace(issued.stdout)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
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
	if sec == nil || sec.Label != "Mail" || sec.URL != "/triage" || len(sec.Tabs) != 2 ||
		sec.Tabs[0].Label != "Decisions" || sec.Tabs[0].URL != "/triage" || sec.Tabs[1].Label != "Rules" || sec.Tabs[1].URL != "/rules" {
		t.Fatalf("Mail section = %+v, want one Mail entry with Decisions and Rules", sec)
	}
	for _, path := range []string{"/triage", "/rules"} {
		status, body, _ := fetch(t, client, http.MethodGet, base+path, nil)
		if status != http.StatusOK || !strings.Contains(body, `aria-label="Sections"`) || !strings.Contains(body, `href="/triage" class="hub-tab"`) || !strings.Contains(body, `href="/rules" class="hub-tab"`) {
			t.Fatalf("GET %s = %d without the Mail sub-navigation: %.400s", path, status, body)
		}
	}

	if got := runApp(t, "settings", "set", module.FlagKey(unit.Mail), "false"); got.code != cli.ExitOK {
		t.Fatalf("turn Mail off: %s", got.stderr)
	}
	waitFor(t, func() bool { return mailSection() == nil })
	if status, body, _ := fetch(t, client, http.MethodGet, base+"/about", nil); status != http.StatusOK || strings.Contains(body, `href="/triage"`) || strings.Contains(body, `href="/rules"`) {
		t.Fatalf("GET /about with Mail off = %d and still links Mail pages: %.400s", status, body)
	}
	for _, path := range []string{"/triage", "/rules"} {
		if status, _, _ := fetch(t, client, http.MethodGet, base+path, nil); status != http.StatusNotFound {
			t.Fatalf("GET %s with Mail off = %d, want 404", path, status)
		}
	}
}
