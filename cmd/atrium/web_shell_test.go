package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestThemeAndRailCookiesShapeTheNextRender(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	base := startServeIn(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	u, _ := url.Parse(base)

	page := func() string {
		t.Helper()
		code, body, _ := fetch(t, client, http.MethodGet, base+"/chat", nil)
		if code != http.StatusOK {
			t.Fatalf("/chat: %d", code)
		}
		return body
	}

	first := page()
	for _, want := range []string{`<html lang="en" data-theme="system">`, `<meta name="color-scheme" content="light dark">`, `data-rail="false"`, `data-theme-value="system" aria-pressed="true"`} {
		if !strings.Contains(first, want) {
			t.Errorf("default render misses %s", want)
		}
	}

	jar.SetCookies(u, []*http.Cookie{{Name: "atrium_theme", Value: "dark", Path: "/"}, {Name: "atrium_rail", Value: "1", Path: "/"}})
	dark := page()
	for _, want := range []string{`<html lang="en" data-theme="dark" class="dark">`, `<meta name="color-scheme" content="dark">`, `data-rail="true"`, `data-theme-value="dark" aria-pressed="true"`, `aria-label="Expand sidebar"`} {
		if !strings.Contains(dark, want) {
			t.Errorf("dark render misses %s", want)
		}
	}

	jar.SetCookies(u, []*http.Cookie{{Name: "atrium_theme", Value: "light", Path: "/"}})
	if light := page(); !strings.Contains(light, `<html lang="en" data-theme="light">`) || !strings.Contains(light, `content="light"`) {
		t.Errorf("light cookie did not render the light theme")
	}

	jar.SetCookies(u, []*http.Cookie{{Name: "atrium_theme", Value: "neon", Path: "/"}})
	if odd := page(); !strings.Contains(odd, `data-theme="system"`) || strings.Contains(odd, "neon") {
		t.Errorf("an unknown theme value was not refused")
	}
}

func TestMainPagesShipNoInlineScript(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	base := startServeIn(t, dir)
	client := &http.Client{}
	scriptTag := regexp.MustCompile(`<script\b[^>]*>`)
	handler := regexp.MustCompile(`\son[a-z]+\s*=\s*["']`)
	for _, path := range []string{"/chat", "/scheduled", "/projects/new", "/library/files", "/library/wiki", "/review", "/activity", "/drafts", "/settings/assistant", "/settings/notifications", "/about"} {
		code, body, header := fetch(t, client, http.MethodGet, base+path, nil)
		if code != http.StatusOK {
			t.Errorf("%s: %d", path, code)
			continue
		}
		csp := header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
			t.Errorf("%s: policy %q", path, csp)
		}
		for _, tag := range scriptTag.FindAllString(body, -1) {
			if !strings.Contains(tag, ` src="/static/`) {
				t.Errorf("%s: inline or foreign script %s", path, tag)
			}
		}
		if m := handler.FindString(body); m != "" {
			t.Errorf("%s: inline event handler %q", path, m)
		}
	}
}
