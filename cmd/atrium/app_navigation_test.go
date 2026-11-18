package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/core/coremodule"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/urlx"
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
	if sec == nil || sec.Label != "Mail" || sec.URL != "/apps/mail/decisions" || len(sec.Tabs) != 3 ||
		sec.Tabs[0].Label != "Decisions" || sec.Tabs[0].URL != "/apps/mail/decisions" || sec.Tabs[1].Label != "Rules" || sec.Tabs[1].URL != "/apps/mail/rules" ||
		sec.Tabs[2].Label != "Status" || sec.Tabs[2].URL != "/apps/mail/mode" {
		t.Fatalf("Mail section = %+v, want one Mail entry with Decisions, Rules and Status", sec)
	}
	for _, path := range []string{"/api/v1/shell", "/api/v1/apps"} {
		got := site.do(http.MethodGet, path, secret, nil)
		if got.status != http.StatusOK {
			t.Fatalf("%s: %d %s", path, got.status, got.body)
		}
		var body any
		if err := json.Unmarshal([]byte(got.body), &body); err != nil {
			t.Fatal(err)
		}
		for _, link := range navURLs(body) {
			at, err := url.Parse(link)
			if err != nil {
				t.Fatalf("%s link %q: %v", path, link, err)
			}
			if _, _, moved := urlx.Moved(at.EscapedPath(), at.RawQuery); moved || at.Path == urlx.LegacyBase || strings.HasPrefix(at.Path, urlx.LegacyBase+"/") {
				t.Errorf("%s links %q, an old server address instead of a client place", path, link)
			}
		}
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
		expectAPI(t, browser.do(http.MethodGet, path, nil), http.StatusNotFound, "not_found")
	}
}

func navURLs(v any) []string {
	var out []string
	switch v := v.(type) {
	case map[string]any:
		for k, item := range v {
			if s, ok := item.(string); ok && (k == "url" || strings.HasSuffix(k, "_url")) && strings.HasPrefix(s, "/") {
				out = append(out, s)
			}
			out = append(out, navURLs(item)...)
		}
	case []any:
		for _, item := range v {
			out = append(out, navURLs(item)...)
		}
	}
	return out
}
