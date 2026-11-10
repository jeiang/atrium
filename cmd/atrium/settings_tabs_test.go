package main

import (
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var settingsTabLabel = regexp.MustCompile(`<a href="/settings/[^"]*"[^>]*>([^<]+)</a>`)

func TestSettingsShowsSixTabsAndDropsAdvancedPages(t *testing.T) {
	t.Parallel()
	base := startServe(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status, body, _ := fetch(t, client, http.MethodGet, base+"/settings/assistant", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /settings/assistant = %d", status)
	}
	start := strings.Index(body, `aria-label="Settings sections"`)
	end := strings.Index(body[max(start, 0):], "</nav>")
	if start < 0 || end < 0 {
		t.Fatalf("no settings tab strip: %.300s", body)
	}
	var labels []string
	for _, m := range settingsTabLabel.FindAllStringSubmatch(body[start:start+end], -1) {
		labels = append(labels, m[1])
	}
	if want := []string{"Assistant", "Mail", "Permissions", "Connections", "Notifications", "Usage"}; !slices.Equal(labels, want) {
		t.Errorf("settings tabs %q, want %q", labels, want)
	}
	for _, path := range []string{"/settings/config", "/settings/config/assistant", "/settings/audit", "/settings/subagents", "/settings/prompts"} {
		if status, _, _ := fetch(t, client, http.MethodGet, base+path, nil); status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, status)
		}
	}
}
