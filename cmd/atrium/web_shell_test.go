package main

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

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
